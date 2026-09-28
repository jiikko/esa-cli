package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 検索の詳細取得（enrich）の失敗を黙って捨てないこと。
// 基本情報のみで残す方針・rc=0 は維持し、失敗件数と最初のエラーを stderr に 1 行出す。
func TestSearchEnrichFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/posts":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body>
<a class="post-title__link" href="/posts/1"><span class="post-title__name">一</span></a>
<a class="post-title__link" href="/posts/2"><span class="post-title__name">二</span></a>
<a class="post-title__link" href="/posts/3"><span class="post-title__name">三</span></a>
</body></html>`))
		case "/posts/1.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":1,"full_name":"カテゴリ/一","updated_at":"2026-01-02T00:00:00+09:00"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	var results []searchResult
	var err error
	_, stderr := captureStdio(t, func() {
		results, err = testClient(srv.URL).search("t", "q", 50, 1, "", true)
	})
	if err != nil {
		t.Fatalf("詳細取得の失敗で検索全体を失敗にしている: %v", err)
	}
	if len(results) != 3 || results[0].FullName != "カテゴリ/一" {
		t.Fatalf("基本情報のみで残す / 成功分は埋める、になっていない: %+v", results)
	}
	if !strings.Contains(stderr, "2/3 件の詳細取得に失敗") || !strings.Contains(stderr, "500") {
		t.Errorf("失敗件数と最初のエラーが stderr に出ていない: %q", stderr)
	}
	if n := strings.Count(strings.TrimRight(stderr, "\n"), "\n"); n != 0 {
		t.Errorf("警告が 1 行でない: %q", stderr)
	}

	// 全件成功なら警告を出さない。
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	t.Cleanup(ok.Close)
	var failed int
	_, stderr = captureStdio(t, func() {
		failed, _ = testClient(ok.URL).enrichResults([]searchResult{{Number: 1}, {Number: 2}}, 2)
	})
	if failed != 0 || stderr != "" {
		t.Errorf("成功時に失敗を報告している: failed=%d stderr=%q", failed, stderr)
	}
}

// レスポンスが上限を超えたら、切り詰めた本文を返さずにエラーにすること。
func TestGetRejectsOversizedResponse(t *testing.T) {
	serve := func(n int) *httptest.Server {
		body := strings.Repeat("a", n)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	if _, _, err := testClient(serve(maxResponseBytes + 1).URL).get("/posts/1.md"); err == nil || !strings.Contains(err.Error(), "サイズ上限") {
		t.Errorf("上限超過を検出していない: %v", err)
	}
	b, _, err := testClient(serve(maxResponseBytes).URL).get("/posts/1.md")
	if err != nil || len(b) != maxResponseBytes {
		t.Errorf("ちょうど上限の本文を拒否した / 欠けた: len=%d err=%v", len(b), err)
	}
}

// 記事番号の解析失敗は使い方の誤り（rc=2）であること。show / meta / revisions は同じ解析を通る。
func TestParseNumberArgRejectsAsUsageError(t *testing.T) {
	blockRealBackends(t) // 退行して通信へ進んでも実環境に届かせない
	for _, raw := range []string{"abc", "0", "https://t.esa.io/posts/abc", "12x"} {
		_, err := parseNumberArg([]string{raw}, "show")
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%q: 使い方の誤りにならない: %T %v", raw, err, err)
		}
	}
	if n, err := parseNumberArg([]string{"https://t.esa.io/posts/12#c"}, "show"); err != nil || n != 12 {
		t.Errorf("正しい入力を拒否した: n=%d err=%v", n, err)
	}
	// サブコマンドの入口から（通信の前に止まる）。
	for _, run := range []func([]string) error{cmdShow, cmdMeta, cmdRevisions} {
		var ue *usageError
		if err := run([]string{"abc"}); !errors.As(err, &ue) {
			t.Errorf("入口で使い方の誤りにならない: %v", err)
		}
	}
}

// search の -n / -page が 1 未満なら使い方の誤りにすること（公式 API の 400 を素通しにしない）。
//
// -team "" を渡しておく: 数値の検査を外す変異では requireTeam の「未設定」に落ちるので、
// 期待する文言で区別できる（かつ通信・Keychain へ進まない）。
func TestSearchRejectsNonPositiveNumbers(t *testing.T) {
	blockRealBackends(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-team", "", "-n", "0", "q"}, "-n は 1 以上"},
		{[]string{"-team", "", "-n", "-3", "q"}, "-n は 1 以上"},
		{[]string{"-team", "", "-page", "0", "q"}, "-page は 1 以上"},
	}
	for _, c := range cases {
		err := cmdSearch(c.args)
		var ue *usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err=%v, want usageError containing %q", c.args, err, c.want)
		}
	}
}

// team はサブドメインとして安全な形だけを受け付けること（外部ホストへ Cookie を送らない /
// キャッシュのパスを cache/ の外へ出さない）。
func TestTeamValidation(t *testing.T) {
	for in, want := range map[string]string{"myteam": "myteam", "my-team": "my-team", "team01": "team01", "MyTeam": "myteam"} {
		got, err := validateTeam(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q err=%v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"x.example#", "evil.example/", "a/../b", "my.team", "myteam.esa.io", " myteam", "a b", "a@b"} {
		var ue *usageError
		if _, err := validateTeam(bad); !errors.As(err, &ue) {
			t.Errorf("%q: 拒否していない: %v", bad, err)
		}
	}

	// requireTeam 経由（全コマンドの入口）。通信の前に止まり、プロファイルを試さないこと。
	env := installFakeProfiles(t, []string{"A"}, map[string]fakeProfile{"A": {url: "http://127.0.0.1:1"}})
	var ue *usageError
	if _, _, err := resolveProfileClient(config{team: "x.example#", profile: profileAuto}); !errors.As(err, &ue) {
		t.Errorf("resolveProfileClient が不正な team を通した: %v", err)
	}
	if len(env.built) != 0 {
		t.Errorf("不正な team でプロファイルを試した: %v", env.built)
	}

	// 大文字の team は正規化した値で先へ進む（baseURL・キャッシュ名は小文字から作る）。
	env = installFakeProfiles(t, []string{"A"}, map[string]fakeProfile{"A": {url: statusServer(t, 200).URL}})
	captureStdio(t, func() { _, _, _ = resolveProfileClient(config{team: "MyTeam", profile: profileAuto}) })
	if strings.Join(env.teams, ",") != "myteam" {
		t.Errorf("正規化した team が使われていない: %v", env.teams)
	}
	if readProfileCache("myteam") != "A" {
		t.Error("キャッシュが正規化した team の名前で書かれていない")
	}

	// config set team は書き込む前に弾き、正しい値は正規化して保存する。
	dir := resetFileConfig(t)
	if err := configSet("team", "x.example#"); !errors.As(err, &ue) {
		t.Errorf("config set team が不正な値を通した: %v", err)
	}
	if _, err := os.Stat(dir + "/esa-cli/config.yml"); err == nil {
		t.Error("不正な team を config.yml に書き込んだ")
	}
	var setErr error
	captureStdio(t, func() { setErr = configSet("team", "MyTeam") })
	b, _ := os.ReadFile(dir + "/esa-cli/config.yml")
	if setErr != nil || !strings.Contains(string(b), "team: myteam") {
		t.Errorf("config set team が正規化した値を保存していない: err=%v\n%s", setErr, b)
	}

	// setup の入力も同じ検査を通る。
	if err := runSetupWithInput(t, "x.example#\n"); !errors.As(err, &ue) {
		t.Errorf("setup が不正な team を通した: %v", err)
	}
}

// 公式 API（ESA_TOKEN 経路）にも get() と同じサイズ上限と 429 の案内があること。
func TestSearchViaAPIResponseHandling(t *testing.T) {
	serve := func(status int, body string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("トークンが送られていない")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		orig := esaAPIBase
		t.Cleanup(func() { esaAPIBase = orig })
		esaAPIBase = srv.URL
	}

	serve(http.StatusTooManyRequests, "")
	if _, err := searchViaAPI("t", "q", 10, 1, "tok"); err == nil || !strings.Contains(err.Error(), "レート制限（429）") {
		t.Errorf("429 がレート制限と分からない: %v", err)
	}

	serve(http.StatusOK, `{"posts":[`+strings.Repeat(" ", maxResponseBytes)+`]}`)
	if _, err := searchViaAPI("t", "q", 10, 1, "tok"); err == nil || !strings.Contains(err.Error(), "サイズ上限") {
		t.Errorf("上限超過を検出していない: %v", err)
	}

	serve(http.StatusOK, `{"posts":[{"number":7,"name":"a&#47;b","full_name":"c/a&#47;b"}]}`)
	rs, err := searchViaAPI("t", "q", 10, 1, "tok")
	if err != nil || len(rs) != 1 || rs[0].Number != 7 || rs[0].Title != "a/b" {
		t.Errorf("正常な応答を読めない: %+v err=%v", rs, err)
	}
}
