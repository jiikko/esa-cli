package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEsa は検索（/posts?q=）と記事 JSON（/posts/N.json）を返す偽の esa。
//
// 検索は本物と同じく in:"<カテゴリ>" を**カテゴリの前方一致**で絞り、番号の昇順で perPage 件ずつ返す。
// 最後のページには rel="next" を付けない（本物の実測どおり）。wrap=true のときは本物の
// 「sort なしで最後より先を要求すると 1 ページ目が返る」形を再現し、rel="next" も常に付ける。
type fakeEsa struct {
	posts    map[int]map[string]any
	perPage  int
	wrap     bool
	failPost map[int]bool
	// onSearch は検索を 1 回受けるたびに（応答を作る前に）呼ばれる。n は 1 始まりの通し番号。
	// ページ送りの途中で記事が消える・増える形の再現に使う。
	onSearch func(f *fakeEsa, n int)

	mu       sync.Mutex
	queries  []string // 検索の q（ページ送りの回数も数える）
	postGets int
}

var inQueryRe = regexp.MustCompile(`in:"([^"]*)"`)

func (f *fakeEsa) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/posts" {
			f.search(w, r)
			return
		}
		var n int
		if _, err := fmt.Sscanf(r.URL.Path, "/posts/%d.json", &n); err == nil {
			f.mu.Lock()
			f.postGets++
			f.mu.Unlock()
			p, ok := f.posts[n]
			if !ok || f.failPost[n] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeEsa) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	f.mu.Lock()
	f.queries = append(f.queries, q)
	n := len(f.queries)
	f.mu.Unlock()
	if f.onSearch != nil {
		f.onSearch(f, n)
	}
	w.Header().Set("Content-Type", "text/html")

	var hits []int
	if m := inQueryRe.FindStringSubmatch(q); m != nil {
		for n, p := range f.posts {
			if cat, _ := p["category"].(string); strings.HasPrefix(cat, m[1]) {
				hits = append(hits, n)
			}
		}
	}
	sort.Ints(hits)
	if len(hits) == 0 {
		_, _ = w.Write([]byte(`<html><body><div class="search__no-result">0 件</div></body></html>`))
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	last := (len(hits) + f.perPage - 1) / f.perPage
	if page > last && f.wrap {
		page = 1
	}
	var sb strings.Builder
	sb.WriteString(`<html><head><link rel="next" href="/ignored"></head><body><ul class="post-list">`)
	for i := (page - 1) * f.perPage; i < len(hits) && i < page*f.perPage; i++ {
		fmt.Fprintf(&sb, `<li><a class="post-title__link" href="/posts/%d"><span class="post-title__name">x</span></a></li>`, hits[i])
	}
	sb.WriteString(`</ul><nav class="pagination">`)
	if page < last || f.wrap {
		fmt.Fprintf(&sb, `<span class="next"><a rel="next" href="/posts?page=%d">Next</a></span>`, page+1)
	}
	sb.WriteString(`</nav></body></html>`)
	_, _ = w.Write([]byte(sb.String()))
}

func post(n int, category, name, body string) map[string]any {
	return map[string]any{
		"number": n, "category": category, "name": name, "body_md": body,
		"updated_at": "2026-09-29T00:00:00+09:00", "updated_by": map[string]any{"screen_name": "someone"},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	return string(b)
}

// runSync は 1 対象を実行し、書き込み（予定）件数・出力・エラーを返す。
// --apply のロックは設定ディレクトリに置かれるので、本物の ~/.config に書かないよう隔離する。
func runSync(t *testing.T, srv *httptest.Server, tg syncTarget, apply bool) (int, string, error) {
	t.Helper()
	if os.Getenv("XDG_CONFIG_HOME") == "" || !strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), os.TempDir()) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	var out strings.Builder
	plan, err := runSyncTarget(testClient(srv.URL), tg, apply, &out)
	return plan.pending(), out.String(), err
}

// カテゴリの木がディレクトリの木へ写り、dry-run は何も書かず、--apply で書き、2 回目は変更なしになること。
// ローカルを編集していれば差分を出して上書きし、権限は保つこと。
func TestSyncWritesCategoryTree(t *testing.T) {
	f := &fakeEsa{perPage: 2, posts: map[int]map[string]any{
		1: post(1, "Users/me/skills/foo", "SKILL", "---\r\nname: foo\r\n---\r\nhello"),
		2: post(2, "Users/me/skills", "README.md", "top"),
		3: post(3, "Users/me/skills2", "other", "隣のカテゴリ（in: の前方一致で当たるが対象外）"),
		4: post(4, "Users/me/skills/foo/refs", "a&#35;b", "escaped"),
	}}
	srv := f.serve(t)
	dir := filepath.Join(t.TempDir(), "out")
	tg := syncTarget{Name: "skills", Category: "Users/me/skills", Dir: dir}

	n, out, err := runSync(t, srv, tg, false)
	if err != nil {
		t.Fatalf("dry-run が失敗: %v", err)
	}
	if n != 3 {
		t.Errorf("dry-run の書き込み予定が %d 件（3 件のはず）\n%s", n, out)
	}
	// 新規のファイルも本文を差分として出す（「    +」は差分の行）
	for _, want := range []string{"+ README.md", "+ foo/SKILL.md", "+ foo/refs/a#b.md", "    +top\n", "    +hello\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run の出力に %q が無い:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other") {
		t.Errorf("隣のカテゴリの記事が対象に入っている:\n%s", out)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry-run で書き出し先を作った/書いた: %v", err)
	}
	if len(f.queries) < 2 {
		t.Fatalf("ページ送りをしていない（検索 %d 回）。perPage=2 なので 2 ページあるはず", len(f.queries))
	}

	if _, _, err := runSync(t, srv, tg, true); err != nil {
		t.Fatalf("apply が失敗: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "foo", "SKILL.md")); got != "---\nname: foo\n---\nhello\n" {
		t.Errorf("本文が改行の正規化どおりでない: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "README.md")); got != "top\n" {
		t.Errorf(".md で終わる記事名の書き出しが違う: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "foo", "refs", "a#b.md")); got != "escaped\n" {
		t.Errorf("記事名の HTML エスケープを戻していない: %q", got)
	}
	var names []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			names = append(names, rel)
		}
		return nil
	})
	if strings.Join(names, ",") != "README.md,foo/SKILL.md,foo/refs/a#b.md" {
		t.Errorf("書かれたファイルの集合が違う（一時ファイルの残骸・隣のカテゴリを含まないこと）: %v", names)
	}

	n, out, err = runSync(t, srv, tg, false)
	if err != nil || n != 0 || !strings.Contains(out, "変更なし 3") {
		t.Errorf("2 回目が変更なしになっていない: n=%d err=%v\n%s", n, err, out)
	}

	skill := filepath.Join(dir, "foo", "SKILL.md")
	if err := os.WriteFile(skill, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(skill, 0o600); err != nil {
		t.Fatal(err)
	}
	n, out, err = runSync(t, srv, tg, false)
	if err != nil || n != 1 {
		t.Fatalf("ローカルの編集を変更として数えていない: n=%d err=%v\n%s", n, err, out)
	}
	for _, want := range []string{"~ foo/SKILL.md", "-local", "+hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("差分の出力に %q が無い:\n%s", want, out)
		}
	}
	if got := readFile(t, skill); got != "local\n" {
		t.Fatalf("dry-run でローカルを書き換えた: %q", got)
	}
	if _, _, err := runSync(t, srv, tg, true); err != nil {
		t.Fatalf("apply が失敗: %v", err)
	}
	if got := readFile(t, skill); got != "---\nname: foo\n---\nhello\n" {
		t.Errorf("上書きしていない: %q", got)
	}
	if fi, _ := os.Stat(skill); fi.Mode().Perm() != 0o600 {
		t.Errorf("既存ファイルの権限を保っていない: %v", fi.Mode().Perm())
	}
}

// 書き出し先がシンボリックリンクなら、その対象は 1 件も書かず、リンク先も触らないこと。
func TestSyncRefusesNonRegularDestination(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "C/foo", "SKILL", "from esa"),
		2: post(2, "C", "README", "top"),
	}}
	srv := f.serve(t)
	base := t.TempDir()
	outside := filepath.Join(base, "outside.md")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "out")
	if err := os.MkdirAll(filepath.Join(dir, "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "foo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	_, _, err := runSync(t, srv, syncTarget{Name: "c", Category: "C", Dir: dir}, true)
	if err == nil || !strings.Contains(err.Error(), "通常のファイルではありません") {
		t.Fatalf("シンボリックリンクの書き出し先を拒否していない: %v", err)
	}
	if got := readFile(t, outside); got != "keep\n" {
		t.Errorf("リンク先を書き換えた: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("問題のある対象で、他のファイルを書いた（1 件も書かないはず）: %v", err)
	}
}

// 記事の取得・対応付けに 1 件でも失敗したら、1 件も書かないこと。
func TestSyncWritesNothingWhenAnyPostFails(t *testing.T) {
	cases := []struct {
		name  string
		posts map[int]map[string]any
		fail  map[int]bool
		want  string
	}{
		{"取得の失敗", map[int]map[string]any{1: post(1, "C", "a", "x"), 2: post(2, "C", "b", "y")},
			map[int]bool{2: true}, "取得できませんでした"},
		{"同じファイルになる 2 記事", map[int]map[string]any{1: post(1, "C", "a", "x"), 2: post(2, "C", "a.md", "y")},
			nil, "同じファイル"},
		{"記事名に /", map[int]map[string]any{1: post(1, "C", "a", "x"), 2: post(2, "C", "b&#47;c", "y")},
			nil, "/ が含まれています"},
		{"カテゴリに ..", map[int]map[string]any{1: post(1, "C", "a", "x"), 2: post(2, "C/..", "b", "y")},
			nil, "不正です"},
		// macOS の既定のファイルシステムでは同じファイルになる組（バイト比較だと両方「新規」で片方が消えた）
		{"大文字小文字だけ違う 2 記事", map[int]map[string]any{1: post(1, "C", "Foo", "x"), 2: post(2, "C", "foo", "y")},
			nil, "同じファイル"},
		{"NFC と NFD だけ違う 2 記事", map[int]map[string]any{1: post(1, "C", "\u304c", "x"), 2: post(2, "C", "\u304b\u3099", "y")},
			nil, "同じファイル"},
		// ToLower では畳まれないが APFS では同じファイルになる組（red team で 1 件だけ書いて失敗・中身の入れ替わりを再現）
		{"ss と ß", map[int]map[string]any{1: post(1, "C", "ss", "x"), 2: post(2, "C", "\u00df", "y")},
			nil, "同じファイル"},
		{"Cherokee の大文字と小文字", map[int]map[string]any{1: post(1, "C", "\u13a0", "x"), 2: post(2, "C", "\uab70", "y")},
			nil, "同じファイル"},
		{"σ と ς", map[int]map[string]any{1: post(1, "C", "\u03c3", "x"), 2: post(2, "C", "\u03c2", "y")},
			nil, "同じファイル"},
		{"大文字の一時ファイルの接尾辞", map[int]map[string]any{1: post(1, "C", "a", "x"), 2: post(2, "C/a.md.ESA-SYNC-TMP", "b", "y")},
			nil, "不正です"},
		{"大文字小文字だけ違うサブカテゴリ", map[int]map[string]any{1: post(1, "C/Dir", "a", "x"), 2: post(2, "C/dir", "a", "y")},
			nil, "同じファイル"},
		// 記事 a.md のファイルと、カテゴリ a.md のディレクトリ（片方を書いた後にもう片方で失敗していた）
		{"ファイルとディレクトリの衝突", map[int]map[string]any{1: post(1, "C", "a.md", "x"), 2: post(2, "C/A.md", "b", "y")},
			nil, "ぶつかります"},
		{"一時ファイルの名前のカテゴリ", map[int]map[string]any{1: post(1, "C", "a", "x"), 2: post(2, "C/a.md.esa-sync-tmp", "b", "y")},
			nil, "不正です"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEsa{perPage: 10, posts: tc.posts, failPost: tc.fail}
			srv := f.serve(t)
			dir := filepath.Join(t.TempDir(), "out")
			_, _, err := runSync(t, srv, syncTarget{Name: "c", Category: "C", Dir: dir}, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("エラーになっていない / 理由が違う（want %q）: %v", tc.want, err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("失敗した対象で書き出し先を作った/書いた: %v", err)
			}
		})
	}
}

// ページ送りは rel="next" の無いページで止まり、1 ページ目に戻る形でも止まり、上限を超えたら切り詰めずにエラーにすること。
func TestListCategoryPostsPaging(t *testing.T) {
	posts := map[int]map[string]any{}
	for n := 1; n <= 5; n++ {
		posts[n] = post(n, "C", fmt.Sprint(n), "")
	}

	f := &fakeEsa{perPage: 2, posts: posts}
	got, err := testClient(f.serve(t).URL).listCategoryPosts("C")
	if err != nil || fmt.Sprint(got) != "[1 2 3 4 5]" {
		t.Fatalf("一覧が違う: %v %v", got, err)
	}
	if len(f.queries) != 6 {
		t.Errorf("最後のページ（3 ページ目）で止まり、確認のためにもう 1 周する形になっていない: 検索 %d 回（3 ページ × 2 周のはず）", len(f.queries))
	}
	if q := f.queries[0]; !strings.Contains(q, `in:"C"`) || !strings.Contains(q, "sort:number-asc") {
		t.Errorf("クエリにカテゴリの絞り込みと並びの固定が無い: %q", q)
	}

	wrap := &fakeEsa{perPage: 2, posts: posts, wrap: true}
	got, err = testClient(wrap.serve(t).URL).listCategoryPosts("C")
	if err != nil || fmt.Sprint(got) != "[1 2 3 4 5]" {
		t.Fatalf("1 ページ目に戻る形で一覧が違う: %v %v", got, err)
	}
	if len(wrap.queries) != 8 {
		t.Errorf("1 ページ目に戻ったページで止まっていない: 検索 %d 回（4 ページ × 2 周のはず）", len(wrap.queries))
	}

	// 上限の検査が無いと無限に回るので、偽サーバ側でも打ち切る（ハングではなく失敗として出す）。
	var endlessHits int
	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endlessHits++; endlessHits > maxSyncPages+10 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<a class="post-title__link" href="/posts/%d">x</a><nav class="pagination"><a rel="next" href="#">n</a></nav>`, page+1)
	}))
	t.Cleanup(endless.Close)
	if _, err := testClient(endless.URL).listCategoryPosts("C"); err == nil || !strings.Contains(err.Error(), "ページを超えました") {
		t.Errorf("ページ送りが終わらないとき、上限でエラーにしていない: %v", err)
	}
}

func TestHasNextPageLink(t *testing.T) {
	cases := []struct {
		name, html string
		want       bool
	}{
		{"pagination の中の rel=next", `<nav class="pagination"><span class="next"><a rel="next" href="?page=2">Next</a></span></nav>`, true},
		{"最後のページ（next が無い）", `<nav class="pagination"><a rel="prev" href="?page=1">Prev</a><span class="page current">2</span></nav>`, false},
		{"head の link rel=next だけ", `<html><head><link rel="next" href="?page=2"></head><body><nav class="pagination"></nav></body></html>`, false},
		{"pagination の外の a rel=next", `<a rel="next" href="?page=2">x</a><nav class="pagination"></nav>`, false},
	}
	for _, tc := range cases {
		if got := hasNextPageLink([]byte(tc.html)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSyncRelPath(t *testing.T) {
	cases := []struct {
		root, category, name string
		want                 string
		inside, wantErr      bool
	}{
		{"R", "R", "README", "README.md", true, false},
		{"R", "R/a/b", "SKILL", "a/b/SKILL.md", true, false},
		{"R", "R", "x.md", "x.md", true, false},
		{"R", "R", "SKILL.MD", "SKILL.MD", true, false}, // 大文字の .MD に .md を足さない
		// ファイル名の上限は UTF-16 で 255。ファイルは一時ファイルの接尾辞（13）と .md（3）を足した長さで測る
		{"R", "R", strings.Repeat("a", 239), strings.Repeat("a", 239) + ".md", true, false},
		{"R", "R", strings.Repeat("a", 240), "", true, true},
		{"R", "R", strings.Repeat("\U0001F600", 119) + "a", strings.Repeat("\U0001F600", 119) + "a.md", true, false}, // 絵文字は 2 単位
		{"R", "R", strings.Repeat("\U0001F600", 120), "", true, true},
		{"R", "R/" + strings.Repeat("b", 255), "x", strings.Repeat("b", 255) + "/x.md", true, false}, // ディレクトリは接尾辞を足さない
		{"R", "R/" + strings.Repeat("b", 256), "x", "", true, true},
		{"R", "R2", "x", "", false, false},
		{"R", "Other/R", "x", "", false, false},
		{"R", "R", "a/b", "", true, true},
		{"R", "R/..", "x", "", true, true},
		{"R", "R//a", "x", "", true, true},
		{"R", "R", "..", "...md", true, false}, // 記事名 .. は ...md という普通のファイル名になる（外へ出ない）
		{"R", "R", ".md", "", true, true},
	}
	for _, tc := range cases {
		got, inside, err := syncRelPath(tc.root, tc.category, tc.name)
		if got != tc.want || inside != tc.inside || (err != nil) != tc.wantErr {
			t.Errorf("syncRelPath(%q, %q, %q) = %q, %v, %v; want %q, %v, err=%v",
				tc.root, tc.category, tc.name, got, inside, err, tc.want, tc.inside, tc.wantErr)
		}
	}
}

func TestParseSyncConfig(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	ok := "sync:\n  - name: skills\n    category: /Users/me/skills/\n    dir: ~/.claude/skills\n"
	got, err := parseSyncConfig([]byte(ok))
	if err != nil || len(got) != 1 || got[0].Category != "Users/me/skills" {
		t.Fatalf("正しい設定を読めない / カテゴリを正規化していない: %+v %v", got, err)
	}
	if empty, err := parseSyncConfig(nil); err != nil || len(empty) != 0 {
		t.Errorf("空のファイルを 0 件として読めない: %v %v", empty, err)
	}
	bad := map[string]string{
		// 他の項目は全部正しく、未知のキーだけがある形（dir の書き間違いの形だと「dir が空」でも落ちるので、
		// 未知のキーの検査を外しても緑のままになる）
		"未知のキー":     "sync:\n  - {name: a, category: C, dir: /x, authors: [me]}\n",
		"名前の重複":     "sync:\n  - {name: a, category: C, dir: /x}\n  - {name: a, category: D, dir: /y}\n",
		"予約語の名前":    "sync:\n  - {name: add, category: C, dir: /x}\n",
		"相対パスの dir": "sync:\n  - {name: a, category: C, dir: rel/x}\n",
		"空のカテゴリ":    "sync:\n  - {name: a, category: /, dir: /x}\n",
		"カテゴリに引用符":  "sync:\n  - {name: a, category: 'C\"', dir: /x}\n",
		"名前に使えない文字": "sync:\n  - {name: 'a b', category: C, dir: /x}\n",
	}
	for name, src := range bad {
		if _, err := parseSyncConfig([]byte(src)); err == nil {
			t.Errorf("%s を拒否していない", name)
		}
	}
}

// 追記は既存のコメントと項目を残し、読めないファイル・重複する名前では 1 バイトも書き換えないこと。
func TestAppendSyncTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "config.yml")
	if err := appendSyncTarget(path, syncTarget{Name: "a", Category: "C", Dir: "/x"}); err != nil {
		t.Fatalf("新規作成できない: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("権限が 0600 でない: %v", fi.Mode().Perm())
	}
	if !strings.HasPrefix(readFile(t, path), "# esa-cli 設定ファイル") {
		t.Errorf("新規作成のヘッダが無い:\n%s", readFile(t, path))
	}

	hand := "# 手で書いたコメント\nsync:\n  - name: a # 行末のコメント\n    category: C\n    dir: /x\n"
	if err := os.WriteFile(path, []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendSyncTarget(path, syncTarget{Name: "b", Category: "D", Dir: "/y"}); err != nil {
		t.Fatalf("追記できない: %v", err)
	}
	out := readFile(t, path)
	for _, want := range []string{"# 手で書いたコメント", "# 行末のコメント"} {
		if !strings.Contains(out, want) {
			t.Errorf("コメント %q が消えた:\n%s", want, out)
		}
	}
	got, err := parseSyncConfig([]byte(out))
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("追記の結果が違う: %+v %v\n%s", got, err, out)
	}

	for name, tc := range map[string]struct {
		content string
		target  syncTarget
	}{
		"名前の重複":     {out, syncTarget{Name: "a", Category: "E", Dir: "/z"}},
		"読めない YAML": {"sync: [\n", syncTarget{Name: "c", Category: "E", Dir: "/z"}},
		"sync が文字列": {"sync: x\n", syncTarget{Name: "c", Category: "E", Dir: "/z"}},
	} {
		if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := appendSyncTarget(path, tc.target); err == nil {
			t.Errorf("%s: 拒否していない", name)
		}
		if readFile(t, path) != tc.content {
			t.Errorf("%s: 拒否したのにファイルを書き換えた", name)
		}
	}

	if err := os.WriteFile(path, []byte("sync:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendSyncTarget(path, syncTarget{Name: "a", Category: "C", Dir: "/x"}); err != nil {
		t.Fatalf("`sync:` だけのファイルに追記できない: %v", err)
	}
	if got, err := parseSyncConfig([]byte(readFile(t, path))); err != nil || len(got) != 1 {
		t.Errorf("`sync:` だけのファイルへの追記の結果が違う: %+v %v", got, err)
	}
}

func TestParseCategoryInput(t *testing.T) {
	cases := map[string]string{
		"https://myteam.esa.io/#path=%2FUsers%2Fme%2Flocal":        "Users/me/local",
		"https://myteam.esa.io/#path=%2F%E6%97%A5%E5%A0%B1%2F&x=1": "日報",
		"https://myteam.esa.io/#path=%2Fa+b%2F100%25":              "a+b/100%", // + を空白にしない / %25 は 1 回だけ戻す
		" /Users/me/skills/ ":                                      "Users/me/skills",
		"Users/me":                                                 "Users/me",
	}
	for in, want := range cases {
		if got, err := parseCategoryInput(in, "myteam"); err != nil || got != want {
			t.Errorf("parseCategoryInput(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// カテゴリの URL でないもの・チームの違う URL は、黙って保存せず使い方エラーにする
	var ue *usageError
	for in, want := range map[string]string{
		"https://myteam.esa.io/posts/123":          "カテゴリの URL ではありません",
		"https://other.esa.io/#path=%2FUsers%2Fme": "設定中のチーム",
	} {
		if _, err := parseCategoryInput(in, "myteam"); !errors.As(err, &ue) || !strings.Contains(err.Error(), want) {
			t.Errorf("parseCategoryInput(%q) を使い方エラー（%s）にしていない: %v", in, want, err)
		}
	}
	if got := defaultSyncName("Users/me/local"); got != "local" {
		t.Errorf("既定の名前が違う: %q", got)
	}
	if got := defaultSyncName("Users/me/日報"); got != "" {
		t.Errorf("名前に使えない文字だけのとき既定を空にしていない: %q", got)
	}
}

func TestUnifiedDiff(t *testing.T) {
	// 変更の間の同一行が 2*ctx（6 行）を超えると hunk が分かれる。ここは 8 行。
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	b := "1\n2\n3\nfour\n5\n6\n7\n8\n9\n10\n11\n12\n13\n"
	want := "--- A\n+++ B\n" +
		"@@ -1,7 +1,7 @@\n 1\n 2\n 3\n-4\n+four\n 5\n 6\n 7\n" +
		"@@ -10,3 +10,4 @@\n 10\n 11\n 12\n+13\n"
	if got := unifiedDiff(a, b, "A", "B", 3); got != want {
		t.Errorf("差分が違う:\ngot:\n%s\nwant:\n%s", got, want)
	}
	// 間が 2*ctx 以下なら 1 つにまとめる（GNU diff -u と同じ）。
	near := "1\n2\n3\nfour\n5\n6\n7\n8\n9\nten\n11\n12\n"
	if got := unifiedDiff(a, near, "A", "B", 3); strings.Count(got, "@@ -") != 1 {
		t.Errorf("間が 6 行の 2 つの変更を 1 つの hunk にまとめていない:\n%s", got)
	}
	if got := unifiedDiff(a, a, "A", "B", 3); got != "" {
		t.Errorf("同じ内容で差分を出した: %q", got)
	}
	if got := unifiedDiff("", "x\n", "A", "B", 3); got != "--- A\n+++ B\n@@ -0,0 +1,1 @@\n+x\n" {
		t.Errorf("空からの差分が違う: %q", got)
	}
}

// ウィザードを非対話で通すと、フラグの値で sync.yml に追記され、同じ名前の 2 回目は拒否されること。
func TestSyncAddWizardNonInteractive(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "Users/me/local", "a", "x"),
		2: post(2, "Users/me/local/b", "c", "y"),
	}}
	srv := f.serve(t)
	installFakeProfiles(t, []string{"P"}, map[string]fakeProfile{"P": {url: srv.URL}})
	t.Setenv("HOME", t.TempDir())
	withEmptyStdin(t)

	args := []string{"-team", "t", "-profile", "P", "-category", "https://t.esa.io/#path=%2FUsers%2Fme%2Flocal", "-dir", "~/out"}
	var err error
	stdout, _ := captureStdio(t, func() { err = syncAdd(args) })
	if err != nil {
		t.Fatalf("非対話の追加が失敗: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "記事が 2 件見つかりました") {
		t.Errorf("カテゴリ配下の件数を表示していない:\n%s", stdout)
	}
	targets, path, err := loadSyncTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("sync.yml に保存されていない: %+v %v", targets, err)
	}
	if want := (syncTarget{Name: "local", Category: "Users/me/local", Dir: "~/out"}); targets[0] != want {
		t.Errorf("保存した内容が違う: %+v, want %+v", targets[0], want)
	}

	before := readFile(t, path)
	withEmptyStdin(t)
	captureStdio(t, func() { err = syncAdd(args) })
	var ue *usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "重複") {
		t.Errorf("同じ名前の 2 回目を使い方エラーで拒否していない: %v", err)
	}
	if readFile(t, path) != before {
		t.Error("拒否したのに sync.yml を書き換えた")
	}

	withEmptyStdin(t)
	captureStdio(t, func() { err = syncAdd([]string{"-team", "t", "-profile", "P", "-dir", "/x"}) })
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "カテゴリは必須") {
		t.Errorf("カテゴリ未入力を使い方エラーにしていない: %v", err)
	}
}

// withEmptyStdin は os.Stdin を即 EOF になるファイルへ差し替える（プロンプトは既定値を採る）。
func withEmptyStdin(t *testing.T) {
	t.Helper()
	fh, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = fh
	t.Cleanup(func() { os.Stdin = orig; _ = fh.Close() })
}

// dir の中を指すリンクが途中にあると、別の記事のディレクトリへ書く形になる。1 件も書かずに止めること。
func TestSyncRefusesSymlinkedParentDirectory(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "C/foo", "SKILL", "foo の本文"),
		2: post(2, "C/bar", "SKILL", "bar の本文"),
	}}
	srv := f.serve(t)
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(dir, "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bar", "SKILL.md"), []byte("bar の本文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bar", filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	_, _, err := runSync(t, srv, syncTarget{Name: "c", Category: "C", Dir: dir}, true)
	if err == nil || !strings.Contains(err.Error(), "ディレクトリではありません") {
		t.Fatalf("途中のリンクを拒否していない: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "bar", "SKILL.md")); got != "bar の本文\n" {
		t.Errorf("リンク越しに別の記事のファイルを書き換えた: %q", got)
	}
}

// 差分を表示した後に書き出し先が変わっていたら、書かずに止めること（手編集を差分に出さないまま消さない）。
func TestWriteSyncFileRefusesWhenDestinationChangedSincePlan(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	dst := filepath.Join(dir, "a.md")
	if err := os.WriteFile(dst, []byte("計画の後の手編集\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := syncFile{rel: "a.md", body: "esa の本文\n"}
	if err := writeSyncFile(root, f, "計画のときの内容\n", true); err == nil {
		t.Fatal("計画の後に変わった書き出し先を上書きした")
	}
	// 食い違いに気づく前にディレクトリを作らないこと（空のディレクトリを残さない）
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "d", "x.md"), []byte("後から\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSyncFile(root, syncFile{rel: "n/e/w.md", body: "x\n"}, "", true); err == nil {
		t.Fatal("計画のときは在ったファイルが無いのに書いた")
	}
	if _, err := os.Stat(filepath.Join(dir, "n")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("食い違いで止まったのにディレクトリを作った: %v", err)
	}
	if got := readFile(t, dst); got != "計画の後の手編集\n" {
		t.Errorf("書き出し先を書き換えた: %q", got)
	}
	if err := writeSyncFile(root, syncFile{rel: "b.md", body: "x\n"}, "", false); err != nil {
		t.Fatalf("計画のとおり無いファイルを書けない: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.md"), []byte("後から作られた\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSyncFile(root, syncFile{rel: "c.md", body: "x\n"}, "", false); err == nil {
		t.Error("計画のときは無かったファイルが後から作られたのに上書きした")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), syncTmpSuffix) {
			t.Errorf("一時ファイルが残った: %s", e.Name())
		}
	}
}

// 大きいファイルでも、手編集した行が dry-run に出ること（行数だけを出して省略しない）。
func TestUnifiedDiffLargeShowsChangedLines(t *testing.T) {
	var a, b strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&a, "line %d\n", i)
		if i == 1000 {
			b.WriteString("手編集した行\n")
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	got := unifiedDiff(a.String(), b.String(), "A", "B", 3)
	for _, want := range []string{"-line 1000\n", "+手編集した行\n", "1000〜1000 行目"} {
		if !strings.Contains(got, want) {
			t.Errorf("大きい差分の要約に %q が無い:\n%s", want, got)
		}
	}
}

// 本文・記事名の制御文字を生で端末へ出さないこと（CR / ESC で行を隠して dry-run の確認をすり抜けられる）。
func TestSyncOutputEscapesControlCharacters(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "C", "a", "悪い指示\r\x1b[2Kただの説明\u202e\U000E0041\u200b\ufe0f\u3164\u115f\u1160\uffa0\u2028\u2029\u034f\U000E0080"),
		2: post(2, "C", "b\x1b[31m", "x"),
	}}
	srv := f.serve(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("前の内容\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, err := runSync(t, srv, syncTarget{Name: "c", Category: "C", Dir: dir}, false)
	if err != nil {
		t.Fatalf("dry-run が失敗: %v", err)
	}
	for _, bad := range []string{"\r", "\x1b", "\u202e", "\U000E0041", "\u200b", "\ufe0f", "\u3164", "\u115f", "\u1160", "\uffa0", "\u2028", "\u2029", "\u034f", "\U000E0080"} {
		if strings.Contains(out, bad) {
			t.Errorf("制御文字 %q を生で出している:\n%q", bad, out)
		}
	}
	for _, want := range []string{`\x{1b}[2K`, `\x{d}`, `b\x{1b}[31m.md`, `\x{202e}`, `\x{e0041}`, `\x{200b}`, `\x{fe0f}`, `\x{3164}`} {
		if !strings.Contains(out, want) {
			t.Errorf("エスケープした形 %q が出ていない:\n%s", want, out)
		}
	}
}

// ページ送りの途中で記事が消えると後ろのページが前へずれて、残っている記事がどのページにも出ない。
// 2 回続けて同じ一覧になるまで取り直して拾うこと。増え続けるなら、切り詰めずにエラーにすること。
func TestListCategoryPostsRetriesWhenListShifts(t *testing.T) {
	posts := map[int]map[string]any{}
	for n := 1; n <= 5; n++ {
		posts[n] = post(n, "C", fmt.Sprint(n), "")
	}
	// 1 周目の 1 ページ目（[1 2]）を返した直後に #2 を消す → 2 ページ目は [4 5] になり #3 が抜ける
	f := &fakeEsa{perPage: 2, posts: posts, onSearch: func(f *fakeEsa, n int) {
		if n == 2 {
			delete(f.posts, 2)
		}
	}}
	got, err := testClient(f.serve(t).URL).listCategoryPosts("C")
	if err != nil || fmt.Sprint(got) != "[1 3 4 5]" {
		t.Fatalf("ずれで抜けた記事を拾えていない: %v %v", got, err)
	}

	next := 100
	growing := &fakeEsa{perPage: 2, posts: map[int]map[string]any{1: post(1, "C", "1", "")}, onSearch: func(f *fakeEsa, n int) {
		f.posts[next] = post(next, "C", fmt.Sprint(next), "")
		next++
	}}
	if _, err := testClient(growing.serve(t).URL).listCategoryPosts("C"); err == nil || !strings.Contains(err.Error(), "安定しませんでした") {
		t.Errorf("一覧が毎回変わるとき、エラーにしていない: %v", err)
	}
}

// 検索には当たったがカテゴリが 1 件も一致しないとき（設定の表記違い）、黙って 0 件にしないこと。
func TestSyncWarnsWhenAllHitsAreExcluded(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{1: post(1, "Users/me2", "a", "x")}}
	_, out, err := runSync(t, f.serve(t), syncTarget{Name: "c", Category: "Users/me", Dir: t.TempDir()}, false)
	if err != nil || !strings.Contains(out, "注意: 検索に当たった 1 件") {
		t.Errorf("全件が除かれたことを知らせていない: %v\n%s", err, out)
	}
}

func TestParseSyncConfigRejectsAmbiguousLayouts(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	bad := map[string]string{
		"複数の文書":          "sync:\n  - {name: a, category: C, dir: /x}\n---\nsync:\n  - {name: b, category: D, dir: /y}\n",
		"同じ dir":         "sync:\n  - {name: a, category: C, dir: /x}\n  - {name: b, category: D, dir: /x/}\n",
		"入れ子の dir":       "sync:\n  - {name: a, category: C, dir: ~/.claude}\n  - {name: b, category: D, dir: ~/.claude/skills}\n",
		"大文字小文字だけ違う dir": "sync:\n  - {name: a, category: C, dir: /X/skills}\n  - {name: b, category: D, dir: /x/Skills}\n",
	}
	for name, src := range bad {
		if _, err := parseSyncConfig([]byte(src)); err == nil {
			t.Errorf("%s を拒否していない", name)
		}
	}
	if _, err := parseSyncConfig([]byte("sync:\n  - {name: a, category: C, dir: /x}\n---\n")); err != nil {
		t.Errorf("末尾の --- だけ（空の文書）を拒否した: %v", err)
	}
	ok := "sync:\n  - {name: a, category: C, dir: ~/.claude/skills}\n  - {name: b, category: D, dir: ~/.claude/skills2}\n"
	if _, err := parseSyncConfig([]byte(ok)); err != nil {
		t.Errorf("名前が前方一致するだけの別の dir を拒否した: %v", err)
	}
}

// sync.yml がリンクなら、リンクを残したまま実体へ追記すること。複数文書のファイルには追記しないこと。
func TestAppendSyncTargetKeepsSymlinkAndRefusesMultiDoc(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real.yml")
	link := filepath.Join(base, "config.yml")
	if err := os.WriteFile(real, []byte("sync:\n  - {name: a, category: C, dir: /x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := appendSyncTarget(link, syncTarget{Name: "b", Category: "D", Dir: "/y"}); err != nil {
		t.Fatalf("リンクの sync.yml に追記できない: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("リンクを通常のファイルに置き換えた: %v", err)
	}
	if got, err := parseSyncConfig([]byte(readFile(t, real))); err != nil || len(got) != 2 {
		t.Errorf("実体に追記されていない: %+v %v", got, err)
	}

	multi := "sync:\n  - {name: a, category: C, dir: /x}\n---\nsync:\n  - {name: b, category: D, dir: /y}\n"
	path := filepath.Join(base, "multi.yml")
	if err := os.WriteFile(path, []byte(multi), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendSyncTarget(path, syncTarget{Name: "c", Category: "E", Dir: "/z"}); err == nil {
		t.Error("複数文書のファイルに追記した（2 つ目以降の文書が消える）")
	}
	if readFile(t, path) != multi {
		t.Error("拒否したのにファイルを書き換えた")
	}
}

// コメントだけの sync.yml に追記しても、手で書いたコメントを残すこと。
func TestAppendSyncTargetKeepsCommentOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("# 自分用のメモ\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendSyncTarget(path, syncTarget{Name: "a", Category: "C", Dir: "/x"}); err != nil {
		t.Fatalf("追記できない: %v", err)
	}
	out := readFile(t, path)
	if !strings.Contains(out, "# 自分用のメモ") {
		t.Errorf("手で書いたコメントが消えた:\n%s", out)
	}
	if got, err := parseSyncConfig([]byte(out)); err != nil || len(got) != 1 {
		t.Errorf("追記の結果が違う: %+v %v\n%s", got, err, out)
	}
}

// 新規ファイルの本文は長ければ先頭と末尾だけを出し、省いた行数を示すこと（上限なしで全行を出さない）。
func TestNewFileSummaryIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	got := newFileSummary(b.String(), "esa #1")
	if n := strings.Count(got, "\n"); n > 2*syncNewFileShow+10 {
		t.Errorf("新規の本文を %d 行出した（上限は先頭と末尾で %d 行）", n, 2*syncNewFileShow)
	}
	for _, want := range []string{"+line 1\n", "+line 1000\n", "700 行略。全 1000 行"} {
		if !strings.Contains(got, want) {
			t.Errorf("要約に %q が無い", want)
		}
	}
	if short := newFileSummary("a\nb\n", "esa #1"); !strings.Contains(short, "+a\n+b\n") || strings.Contains(short, "略") {
		t.Errorf("短い本文を全行出していない: %q", short)
	}
}

// 同じ dir への --apply は同時に 1 本だけ（2 本目は何も書かずにエラー）。ロックを外せば次が取れること。
func TestSyncApplyIsExclusivePerDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "out")
	l1, err := acquireSyncLock(dir)
	if err != nil {
		t.Fatalf("1 本目のロックを取れない: %v", err)
	}
	// 大文字小文字だけ違うパスも同じディレクトリ（APFS）なので同じロック
	if _, err := acquireSyncLock(filepath.Join(filepath.Dir(dir), "OUT")); err == nil || !strings.Contains(err.Error(), "書き込み中") {
		t.Fatalf("同じ dir の 2 本目を拒否していない: %v", err)
	}
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{1: post(1, "C", "a", "x")}}
	if _, _, err := runSync(t, f.serve(t), syncTarget{Name: "c", Category: "C", Dir: dir}, true); err == nil {
		t.Fatal("ロック中の dir に --apply が書いた")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ロックで止まったのに書き出し先を作った/書いた: %v", err)
	}
	if n, _, err := runSync(t, f.serve(t), syncTarget{Name: "c", Category: "C", Dir: dir}, false); err != nil || n != 1 {
		t.Errorf("dry-run はロックを取らずに動くはず: n=%d %v", n, err)
	}
	l1.release()
	if _, _, err := runSync(t, f.serve(t), syncTarget{Name: "c", Category: "C", Dir: dir}, true); err != nil {
		t.Fatalf("ロックを外した後の --apply が失敗: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.md")); got != "x\n" {
		t.Errorf("書かれていない: %q", got)
	}
}

// 中断で残った一時ファイルは、dry-run で知らせ、--apply で消すこと（変更なしのファイルの分も）。
// 計画に載っていないファイルの一時ファイルには触れないこと（走査して消さない）。
func TestSyncCleansLeftoverTempFilesOfPlannedFiles(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{1: post(1, "C", "a", "same")}}
	srv := f.serve(t)
	dir := t.TempDir()
	for name, body := range map[string]string{"a.md": "same\n", "a.md" + syncTmpSuffix: "中断の残骸", "other.md" + syncTmpSuffix: "計画外"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tg := syncTarget{Name: "c", Category: "C", Dir: dir}
	n, out, err := runSync(t, srv, tg, false)
	if err != nil || n != 0 || !strings.Contains(out, "一時ファイルが 1 件あります") {
		t.Fatalf("dry-run で残骸を知らせていない: n=%d %v\n%s", n, err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.md"+syncTmpSuffix)); err != nil {
		t.Fatalf("dry-run で残骸を消した: %v", err)
	}
	if _, out, err = runSync(t, srv, tg, true); err != nil || !strings.Contains(out, "1 件消しました") {
		t.Fatalf("--apply で残骸を消していない: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.md"+syncTmpSuffix)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("変更なしのファイルの残骸が残った: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.md"+syncTmpSuffix)); err != nil {
		t.Errorf("計画に無いファイルの一時ファイルを消した: %v", err)
	}
}

// ウィザードの入力の誤り（dir の重なり・記事の URL）は、esa に問い合わせる前に使い方エラーで止め、何も保存しないこと。
func TestSyncAddRejectsBadInputBeforeQueryingEsa(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{1: post(1, "C", "a", "x")}}
	srv := f.serve(t)
	installFakeProfiles(t, []string{"P"}, map[string]fakeProfile{"P": {url: srv.URL}})
	t.Setenv("HOME", t.TempDir())
	path, _ := configFilePath()
	if err := appendSyncTarget(path, syncTarget{Name: "skills", Category: "C", Dir: "~/.claude/skills"}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, path)
	for name, args := range map[string][]string{
		"入れ子の dir":  {"-category", "D", "-dir", "~/.claude/skills/sub", "-name", "sub"},
		"同じ dir":    {"-category", "D", "-dir", "~/.claude/skills/", "-name", "other"},
		"記事の URL":   {"-category", "https://t.esa.io/posts/123", "-dir", "/x"},
		"別チームの URL": {"-category", "https://other.esa.io/#path=%2FC", "-dir", "/x"},
	} {
		f.queries = nil
		withEmptyStdin(t)
		var err error
		captureStdio(t, func() { err = syncAdd(append([]string{"-team", "t", "-profile", "P"}, args...)) })
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: 使い方エラー（rc=2）にしていない: %v", name, err)
		}
		if len(f.queries) != 0 {
			t.Errorf("%s: 拒否する前に esa へ問い合わせた（検索 %d 回）", name, len(f.queries))
		}
		if readFile(t, path) != before {
			t.Errorf("%s: 拒否したのに sync.yml を書き換えた", name)
		}
	}
}

// esa sync help / esa sync add help はヘルプを stdout へ出すこと（esa config help と揃える）。
func TestSyncHelpSubcommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct {
		run  func() error
		want string
	}{
		{func() error { return cmdSync([]string{"help"}) }, syncHelp},
		{func() error { return syncAdd([]string{"help"}) }, syncAddHelp},
	} {
		var err error
		stdout, stderr := captureStdio(t, func() { err = tc.run() })
		if err != nil || stdout != tc.want || stderr != "" {
			t.Errorf("help が stdout に出ていない: err=%v stderr=%q stdout 先頭=%q", err, stderr, stdout[:min(len(stdout), 40)])
		}
	}
}

// 🚨 同じ実体の dir を、実パスとシンボリックリンクのパスの 2 つで指しても同じロックになること。
// 文字列のまま鍵にすると 2 本の --apply が同じ dir に同時に書けた（v0.1.7 で実測）。
// まだ無い dir（初回の --apply）でも、存在する親までを解決して同じ鍵になること。
func TestSyncLockResolvesSymlinks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ held, other string }{
		{real, link}, // 既にある dir
		{filepath.Join(real, "sub"), filepath.Join(link, "sub")},   // まだ無い dir（親がリンク）
		{filepath.Join(link, "sub2"), filepath.Join(real, "sub2")}, // 逆向き
	} {
		l, err := acquireSyncLock(c.held)
		if err != nil {
			t.Fatalf("%s のロックを取れない: %v", c.held, err)
		}
		if l2, err := acquireSyncLock(c.other); err == nil || !strings.Contains(err.Error(), "書き込み中") {
			if l2 != nil {
				l2.release()
			}
			t.Errorf("%s を持っている間に、同じ実体の %s のロックが取れた: %v", c.held, c.other, err)
		}
		l.release()
	}
}

// リンク先がまだ無いリンク（~/.claude/skills → ~/dotfiles/skills を張ったが実体はまだ無い初回）でも、
// リンクのパスと実体のパスが同じロックになること（敵対的レビューの P2-1）。
func TestSyncLockResolvesDanglingSymlink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := t.TempDir()
	missing := filepath.Join(base, "missing") // まだ作らない
	link := filepath.Join(base, "link")
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	l, err := acquireSyncLock(filepath.Join(missing, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	if l2, err := acquireSyncLock(filepath.Join(link, "sub")); err == nil {
		l2.release()
		t.Error("リンク先がまだ無いリンクのパスで、同じ実体のロックが取れた")
	}
}

// 親がリンクで、その中のリンクが ".." を含む相対ターゲット（リンク先はまだ無い）のとき、実体と同じ鍵になること。
// 例: ~/.claude → ~/dotfiles/claude の中で skills → ../skills を張ったが実体はまだ無い初回（敵対的レビュー 2 周目）。
func TestSyncLockResolvesRelativeDanglingUnderLinkedParent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := t.TempDir()
	inner := filepath.Join(base, "deep", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, filepath.Join(base, "L1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../missing", filepath.Join(inner, "link")); err != nil {
		t.Fatal(err)
	}
	l, err := acquireSyncLock(filepath.Join(base, "deep", "missing", "sub")) // カーネルが解決する実体
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	if l2, err := acquireSyncLock(filepath.Join(base, "L1", "link", "sub")); err == nil {
		l2.release()
		t.Error("親がリンク・相対の .. を含むリンクのパスで、同じ実体のロックが取れた")
	}
}

// ロックを取った後にリンクが指し直されても、同じ文字列で登録した 2 本目は取れないこと
// （実パスの鍵だけにすると 2 本目は指し直した先の鍵を取れてしまう。敵対的レビューの P2-2。旧実装では排他だった）。
func TestSyncLockSurvivesSymlinkRetarget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := t.TempDir()
	x, y := filepath.Join(base, "x"), filepath.Join(base, "y")
	for _, d := range []string{x, y} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(x, link); err != nil {
		t.Fatal(err)
	}
	l, err := acquireSyncLock(link)
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(y, link); err != nil {
		t.Fatal(err)
	}
	if l2, err := acquireSyncLock(link); err == nil {
		l2.release()
		t.Error("リンクを指し直した後、同じ文字列のパスの 2 本目がロックを取れた")
	}
}

// ロック用のディレクトリを作れないとき、何のためのディレクトリかをエラーに出すこと。
func TestSyncLockErrorExplainsPurpose(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "esa-cli"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(cfg, "esa-cli"), 0o755) })
	if _, err := acquireSyncLock("/x"); err == nil || !strings.Contains(err.Error(), "同時実行を防ぐ") {
		t.Errorf("ロックのエラーに目的が書かれていない: %v", err)
	}
}

// `esa sync -apply add` のようにサブコマンドをフラグの後ろに書くと、add が対象の名前として扱われていた。
// 予約名は対象に使えないので、書き方の誤りとして使い方エラーにすること。
func TestSyncRejectsSubcommandAfterFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"-apply", "add"}, {"-apply", "list"}, {"-team", "t", "help"}} {
		var err error
		captureStdio(t, func() { err = cmdSync(args) })
		var ue *usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "直後に書いてください") {
			t.Errorf("esa sync %v を書き方の誤りとして扱っていない: %v", args, err)
		}
	}
}

// --apply を繰り返しても、開いたままのファイル記述子と goroutine が増えないこと。
//
// 🚨 計測中は GC を止める。閉じ忘れた os.Root / os.File は GC の後始末（finalizer）が閉じるので、GC が走ると
// 漏れが見えない（実測: GC を止めずに root.Close() を外しても fd は増えなかった。止めると 200 回で 11 → 171）。
// fd は lsof で数える（macOS の /dev/fd の一覧はこのプロセスの fd を数えられなかった）。
func TestSyncDoesNotLeakDescriptorsOrGoroutines(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Fatalf("lsof が無いので fd を数えられない（macOS 専用の repo なので在るはず）: %v", err)
	}
	lsofCount := func() int {
		out, err := exec.Command("lsof", "-n", "-P", "-p", fmt.Sprint(os.Getpid())).Output()
		if err != nil {
			t.Fatalf("lsof が失敗: %v", err)
		}
		return strings.Count(string(out), "\n") - 1
	}
	// 🚨 待機中の keep-alive 接続（クライアント側とプロセス内の fake サーバ側の 2 本ずつ）を数えない。
	// 数えると、プールに何本待機しているかで数が揺れて落ちる（issue 012。CI で fd 19 → 22）。
	// 閉じるのは待機中のものだけなので、body を閉じ忘れて使い中のまま漏れた接続は残り、検出できる。
	// サーバ側はクライアントが閉じたのを受けて非同期に閉じるので、数が落ち着くまで待つ。
	countFDs := func() int {
		sharedTransport.CloseIdleConnections()
		prev := lsofCount()
		for range 50 {
			time.Sleep(20 * time.Millisecond)
			n := lsofCount()
			if n == prev {
				return n
			}
			prev = n
		}
		t.Fatalf("fd の数が 1 秒待っても落ち着かない（最後 %d）", prev)
		return 0
	}
	posts := map[int]map[string]any{}
	for n := 1; n <= 20; n++ {
		posts[n] = post(n, fmt.Sprintf("C/d%d", n%4), fmt.Sprint(n), fmt.Sprintf("body %d", n))
	}
	f := &fakeEsa{perPage: 7, posts: posts, failPost: map[int]bool{}}
	srv := f.serve(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "out")
	tg := syncTarget{Name: "c", Category: "C", Dir: dir}
	applied := 0
	run := func(i int) {
		if i%10 == 6 { // 書き出し先が無い状態からの --apply（作ってから開き直す経路）も混ぜる
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		if i%5 == 3 { // 取得の失敗の経路も混ぜる
			f.failPost[7] = true
		} else {
			delete(f.failPost, 7)
		}
		if i%4 == 0 { // 変更の経路も混ぜる
			_ = os.WriteFile(filepath.Join(dir, "d1", "1.md"), []byte(fmt.Sprint("edit ", i)), 0o644)
		}
		if _, _, err := runSync(t, srv, tg, i%2 == 0); err == nil && i%2 == 0 {
			applied++
		}
	}
	for i := 0; i < 4; i++ { // 接続プールなどの初期化を済ませる
		run(i)
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	fd0, g0 := countFDs(), runtime.NumGoroutine()
	applied = 0
	for i := 4; i < 104; i++ {
		run(i)
	}
	fd1, g1 := countFDs(), runtime.NumGoroutine()
	if applied < 20 {
		t.Fatalf("--apply がほとんど成功していない（%d 回）。漏れを測る経路を通っていない", applied)
	}
	if fd1 > fd0+2 || g1 > g0+2 {
		t.Errorf("100 回の実行で増えた: fd %d → %d / goroutine %d → %d", fd0, fd1, g0, g1)
	}
}
