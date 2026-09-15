package main

import (
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 資格情報を持ち越すリダイレクトを止めること。
//
// 🚨 Go の既定は 3xx を追い、Cookie / Authorization は**別ドメインへは剥がれる**が
// **https→http のダウングレードでは剥がれない**（stdlib はホスト名しか比較せず
// scheme を見ない）。esa は GET で読むだけなので、同一ホストの https リダイレクトは
// 追ってよいが、scheme とホストの変更は止める。
func TestHTTPClientRedirectPolicy(t *testing.T) {
	policy := newHTTPClient().CheckRedirect
	if policy == nil {
		t.Fatal("CheckRedirect が設定されていない（既定のまま追ってしまう）")
	}

	mkReq := func(rawurl string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, rawurl, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	cases := []struct {
		name    string
		from    string
		to      string
		wantErr string // 空なら許可
	}{
		{name: "同一ホスト・同一 scheme は追う", from: "https://team.esa.io/posts/1", to: "https://team.esa.io/posts/1-slug"},
		{name: "https→http のダウングレードは止める", from: "https://team.esa.io/posts/1", to: "http://team.esa.io/posts/1", wantErr: "scheme が変わりました"},
		{name: "別ホストは止める", from: "https://team.esa.io/posts/1", to: "https://evil.example.jp/collect", wantErr: "ホストが変わりました"},
		{name: "サブドメインの変更も止める", from: "https://team.esa.io/posts/1", to: "https://other.esa.io/x", wantErr: "ホストが変わりました"},
	}
	for _, c := range cases {
		err := policy(mkReq(c.to), []*http.Request{mkReq(c.from)})
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: 追うべきだが止めた: %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: 止めるべきだが追った", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 案内文が違う: %v", c.name, err)
		}
	}

	t.Run("リダイレクトの回数に上限がある", func(t *testing.T) {
		var via []*http.Request
		for i := 0; i < 5; i++ {
			via = append(via, mkReq("https://team.esa.io/a"))
		}
		if err := policy(mkReq("https://team.esa.io/b"), via); err == nil {
			t.Error("上限を超えても止まらない")
		}
	})

	t.Run("実際のリダイレクトを追える（回帰）", func(t *testing.T) {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/moved", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()

		resp, err := newHTTPClient().Get(srv.URL + "/")
		if err != nil {
			t.Fatalf("同一ホストのリダイレクトは追うべき: %v", err)
		}
		defer resp.Body.Close()
		if hits != 2 {
			t.Errorf("リダイレクト先まで到達していない: hits=%d", hits)
		}
	})
}

// 検索クエリの後ろに置いたフラグを検出すること。
func TestCheckNoTrailingFlags(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("search", flag.ContinueOnError)
		fs.String("c", "", "columns")
		fs.Int("n", 0, "count")
		fs.Bool("fast", false, "fast")
		return fs
	}
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "クエリだけ", args: []string{"in:設計", "キーワード"}},
		{name: "esa の除外検索は誤検出しない", args: []string{"-退職"}},
		{name: "後ろに -c", args: []string{"キーワード", "-c", "number"}, wantErr: true},
		{name: "後ろに -fast", args: []string{"キーワード", "-fast"}, wantErr: true},
		{name: "後ろに --n=5", args: []string{"キーワード", "--n=5"}, wantErr: true},
	}
	for _, c := range cases {
		err := checkNoTrailingFlags(newFS(), c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
		if c.wantErr && err != nil {
			var ue *usageError
			if !errors.As(err, &ue) {
				t.Errorf("%s: 使い方エラー（rc=2）にすべき: %T", c.name, err)
			}
		}
	}
}

// TSV の値とヘッダを無害化すること（タブ・改行で列がずれない、ESC を端末へ流さない）。
func TestRenderTableSanitizesCells(t *testing.T) {
	results := []searchResult{
		{Number: 1, FullName: "カテゴリ/タブ\tを含む\nタイトル"},
		{Number: 2, FullName: "\x1b]0;PWNED\x07普通のタイトル"},
	}
	out := renderTable(results, []string{"number", "name"}, true)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 { // ヘッダ + 2 行
		t.Fatalf("行数が違う（改行が潰れていない）: %d\n%q", len(lines), out)
	}
	for i, line := range lines {
		if n := strings.Count(line, "\t"); n != 1 {
			t.Errorf("%d 行目のタブが %d 個（1 個であるべき）: %q", i+1, n, line)
		}
	}
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
		t.Errorf("制御文字が残っている: %q", out)
	}
}

// 終了コードの出し分け。
func TestExitCodeFor(t *testing.T) {
	if got := exitCodeFor(nil); got != 0 {
		t.Errorf("成功: got %d", got)
	}
	if got := exitCodeFor(&usageError{"bad"}); got != 2 {
		t.Errorf("使い方の誤り: got %d, want 2", got)
	}
	if got := exitCodeFor(errors.New("boom")); got != 1 {
		t.Errorf("実行時エラー: got %d, want 1", got)
	}
	if got := exitCodeFor(&errSessionExpired{url: "https://x"}); got != 1 {
		t.Errorf("セッション切れ: got %d, want 1", got)
	}
}
