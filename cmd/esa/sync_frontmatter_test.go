package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// front matter の metadata.esa-sync（issue 013）。見つけ方・書き損じの扱い・末尾のコメントとの併用。
func TestFindSyncDirectiveFrontMatter(t *testing.T) {
	const fm = "---\nname: nr\nmetadata:\n  esa-sync: nr/SKILL.md\n---\nbody\n"
	cases := []struct {
		name, body string
		spec, rest string
		found      bool
		wantErr    string // エラー文に含まれるべき文字列（空ならエラーなし）
	}{
		{"metadata の指定。本文は取り除かない", fm, "nr/SKILL.md", fm, true, ""},
		{"引用符付きの文字列", "---\nmetadata:\n  esa-sync: \"a/SKILL.md\"\n---\n", "a/SKILL.md", "---\nmetadata:\n  esa-sync: \"a/SKILL.md\"\n---\n", true, ""},
		{"flow 形式の metadata", "---\nmetadata: {esa-sync: a.md, other: 1}\n---\n", "a.md", "---\nmetadata: {esa-sync: a.md, other: 1}\n---\n", true, ""},
		{"metadata に指定が無い", "---\nname: x\nmetadata:\n  owner: me\n---\nbody\n", "", "---\nname: x\nmetadata:\n  owner: me\n---\nbody\n", false, ""},
		{"front matter の無い記事", "body\n", "", "body\n", false, ""},
		{"閉じの無い --- に本文がない", "---\nname: x\nbody\n", "", "---\nname: x\nbody\n", false, ""},
		{"閉じ忘れの中の指定", "---\nmetadata:\n  esa-sync: a.md\nbody\n", "", "", false, "閉じの --- がありません"},
		{"閉じ忘れの中の指定（最後の行）", "---\nmetadata:\n  esa-sync: a.md\n", "", "", false, "閉じの --- がありません"},
		{"1 行目が --- でなければ front matter でない", "\n---\nmetadata:\n  esa-sync: a.md\n---\n", "", "\n---\nmetadata:\n  esa-sync: a.md\n---\n", false, ""},
		{"本文の途中の YAML の例は見ない", "body\n```yaml\nesa-sync: a.md\n```\n", "", "body\n```yaml\nesa-sync: a.md\n```\n", false, ""},
		{"description の中の esa-sync の説明は止めない", "---\ndescription: \"esa-sync: a.md の書き方\"\n---\n", "", "---\ndescription: \"esa-sync: a.md の書き方\"\n---\n", false, ""},
		{"コメントの指定（互換）は今までどおり", "---\nname: x\n---\nbody\n<!-- esa-sync: a.md -->\n", "a.md", "---\nname: x\n---\nbody\n", true, ""},
		// 書き損じ
		{"最上位に置いた", "---\nesa-sync: a.md\n---\n", "", "", false, "metadata の下に"},
		{"下線", "---\nmetadata:\n  esa_sync: a.md\n---\n", "", "", false, "metadata.esa_sync"},
		{"キャメルケース", "---\nmetadata:\n  esaSync: a.md\n---\n", "", "", false, "metadata の下に"},
		{"大文字の Metadata", "---\nMetadata:\n  esa-sync: a.md\n---\n", "", "", false, "Metadata.esa-sync"},
		{"一段深い", "---\nmetadata:\n  x:\n    esa-sync: a.md\n---\n", "", "", false, "metadata.x.esa-sync"},
		{"metadata がシーケンス", "---\nmetadata:\n  - esa-sync: a.md\n---\n", "", "", false, "metadata の下に"},
		{"値が空", "---\nmetadata:\n  esa-sync:\n---\n", "", "", false, "パスの文字列"},
		{"値が数値", "---\nmetadata:\n  esa-sync: 1\n---\n", "", "", false, "パスの文字列"},
		{"値がマップ", "---\nmetadata:\n  esa-sync:\n    path: a.md\n---\n", "", "", false, "パスの文字列"},
		{"キーの重複", "---\nmetadata:\n  esa-sync: a.md\n  esa-sync: b.md\n---\n", "", "", false, "2 個あります"},
		{"metadata の重複", "---\nmetadata:\n  esa-sync: a.md\nmetadata:\n  esa-sync: b.md\n---\n", "", "", false, "2 個あります"},
		{"壊れた YAML の中の指定", "---\nmetadata:\n  esa-sync: a.md\n bad: [\n---\n", "", "", false, "YAML が読めません"},
		{"両方に指定", "---\nmetadata:\n  esa-sync: a.md\n---\nbody\n<!-- esa-sync: b.md -->\n", "", "", false, "2 つにあります"},
		{"アンカーの定義の側の esa-sync", "---\nx: &a\n  esa-sync: a.md\nmetadata: *a\n---\n", "", "", false, "x.esa-sync"},
		{"値がエイリアス", "---\np: &p a/SKILL.md\nmetadata:\n  esa-sync: *p\n---\n", "a/SKILL.md", "---\np: &p a/SKILL.md\nmetadata:\n  esa-sync: *p\n---\n", true, ""},
		{"パスの -- は通す（コメントではない）", "---\nmetadata:\n  esa-sync: a--b/SKILL.md\n---\n", "a--b/SKILL.md", "---\nmetadata:\n  esa-sync: a--b/SKILL.md\n---\n", true, ""},
		// red team: 誤検出（正当な記事を止めない）
		{"水平線で始まる記事とコメントの指定", "---\n本文\n<!-- esa-sync: a.md -->\n", "a.md", "---\n本文\n", true, ""},
		{"水平線で始まる記事の英文", "---\nThe worker processes async jobs.\n", "", "---\nThe worker processes async jobs.\n", false, ""},
		{"水平線で始まる記事の説明の文", "---\nこの記事は esa sync で書き出す手順\n", "", "---\nこの記事は esa sync で書き出す手順\n", false, ""},
		{"別の用途のキー", "---\nmetadata:\n  uses_async: true\n  processes-async: a.md\n---\n", "", "---\nmetadata:\n  uses_async: true\n  processes-async: a.md\n---\n", false, ""},
		{"水平線に挟まれた普通の文", "---\nesa sync: 書き出しのコマンド\n---\n本文\n", "", "---\nesa sync: 書き出しのコマンド\n---\n本文\n", false, ""},
		{"壊れた YAML とコメントの指定", "---\nname: x\ndescription: Use when: esa sync を打つ\n---\nbody\n<!-- esa-sync: a.md -->\n", "a.md", "---\nname: x\ndescription: Use when: esa sync を打つ\n---\nbody\n", true, ""},
		{"閉じの無い --- の中の別の .md のキー", "---\nlink: docs/a.md\n本文\n", "", "---\nlink: docs/a.md\n本文\n", false, ""},
		{"壊れた YAML の中の別の .md のキー", "---\nfile: a.md\nbad: [\n---\n", "", "---\nfile: a.md\nbad: [\n---\n", false, ""},
		// red team 2 周目
		{"閉じ忘れの中の指定（行末のコメント）", "---\nname: x\nmetadata:\n  esa-sync: a/SKILL.md # 書き出し先\nbody\n", "", "", false, "閉じの --- がありません"},
		{"閉じ忘れの中の指定（フロー形式）", "---\nmetadata: {esa-sync: a/SKILL.md}\nbody\n", "", "", false, "閉じの --- がありません"},
		{"閉じ忘れの中の指定（空白入りのパス）", "---\nmetadata:\n  esa-sync: \"my skill/SKILL.md\"\nbody\n", "", "", false, "閉じの --- がありません"},
		{"1 行に書いた metadata（壊れた YAML）", "---\nmetadata: esa-sync: a/SKILL.md\n---\n", "", "", false, "YAML が読めません"},
		{"水平線に挟まれた文の .md", "---\n詳しくは esa sync: README.md\n---\n本文\n", "", "---\n詳しくは esa sync: README.md\n---\n本文\n", false, ""},
		{"水平線に挟まれた文の .md とコメントの指定", "---\n詳しくは esa sync: README.md\n---\n本文\n<!-- esa-sync: a.md -->\n", "a.md", "---\n詳しくは esa sync: README.md\n---\n本文\n", true, ""},
		{"水平線で始まる解説記事の例（空行の後）", "---\n\n- 例\n  esa-sync: foo/SKILL.md\n\n以上\n", "", "---\n\n- 例\n  esa-sync: foo/SKILL.md\n\n以上\n", false, ""},
		{"壊れた YAML の中の説明の文（esa sync の直後がコロンでない）", "---\nesa sync の使い方: README.md を参照\n- 注意\n---\n本文\n", "", "---\nesa sync の使い方: README.md を参照\n- 注意\n---\n本文\n", false, ""},
		{"閉じ忘れの中の指定（コロンの後の空白なし）", "---\nmetadata:\n  esa-sync:a/SKILL.md\nbody\n", "", "", false, "閉じの --- がありません"},
		// red team: 黙って戻る
		{"BOM で始まる", "\ufeff---\nmetadata:\n  esa-sync: a.md\n---\n", "", "", false, "1 行目は --- だけに"},
		{"1 行目の後ろに空白", "--- \nmetadata:\n  esa-sync: a.md\n---\n", "", "", false, "1 行目は --- だけに"},
		{"1 行目の後ろにタブ", "---\t\nmetadata:\n  esa-sync: a.md\n---\n", "", "", false, "1 行目は --- だけに"},
		{"中に --- の行（文書が 2 つ）", "---\nname: x\n--- \nmetadata:\n  esa-sync: a.md\n---\n", "", "", false, "文書が 2 つ以上"},
	}
	for _, tc := range cases {
		spec, rest, found, err := findSyncDirective(tc.body)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v; want %q を含むエラー", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if spec != tc.spec || rest != tc.rest || found != tc.found {
			t.Errorf("%s: findSyncDirective(%q) = %q, %q, %v; want %q, %q, %v", tc.name, tc.body, spec, rest, found, tc.spec, tc.rest, tc.found)
		}
	}
}

// 壊れた YAML でも、指定らしい行が無ければ関わらない（Claude Code も「フィールドなし」で読む）。
func TestFindSyncDirectiveBrokenYAMLWithoutKey(t *testing.T) {
	body := "---\nname: [\n---\nbody\n"
	spec, rest, found, err := findSyncDirective(body)
	if err != nil || found || spec != "" || rest != body {
		t.Errorf("findSyncDirective(%q) = %q, %q, %v, %v", body, spec, rest, found, err)
	}
}

// front matter の指定の記事が、偽の esa から front matter ごと書き出され、dry-run に「指定による」と出ること。
// パスの検査はコメントの指定と同じ関数（syncDirectiveRelPath）を通る。
func TestSyncWritesToFrontMatterDirectivePath(t *testing.T) {
	body := "---\nname: nr\nmetadata:\n  esa-sync: nr/SKILL.md\n---\nbody\n"
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "Users/me/skills", "月次 New Relic 監査", body),
		2: post(2, "Users/me/skills/team", "何か", "---\nmetadata:\n  esa-sync: ../x.md\n---\n"),
	}}
	srv := f.serve(t)
	dir := t.TempDir()
	tg := syncTarget{Name: "skills", Category: "Users/me/skills", Dir: dir}

	// 上へ出る指定があれば、対象の全体を書かない
	if _, out, err := runSync(t, srv, tg, false); err == nil || !strings.Contains(err.Error(), "パスの要素") {
		t.Fatalf("../ の指定でエラーにならない: err=%v\n%s", err, out)
	}
	delete(f.posts, 2)

	n, out, err := runSync(t, srv, tg, false)
	if err != nil || n != 1 {
		t.Fatalf("dry-run: n=%d err=%v\n%s", n, err, out)
	}
	if !strings.Contains(out, "+ nr/SKILL.md  新規 esa #1（書き出し先は本文の指定による）") {
		t.Errorf("dry-run に指定による書き出し先が出ていない:\n%s", out)
	}
	if _, out, err := runSync(t, srv, tg, true); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got := readFile(t, filepath.Join(dir, "nr", "SKILL.md")); got != body {
		t.Errorf("nr/SKILL.md = %q; want %q（front matter を取り除かない）", got, body)
	}
	if n, out, err := runSync(t, srv, tg, false); err != nil || n != 0 {
		t.Errorf("2 回目は変更なしのはず: n=%d err=%v\n%s", n, err, out)
	}
}

// front matter の指定もパスの検査（syncDirectiveRelPath）を通る。-- を禁じるのはコメントの指定だけ（issue 013）。
func TestMapSyncFilesWithFrontMatterDirective(t *testing.T) {
	cases := []struct {
		name, body, want, wantErr string
	}{
		{"-- を含むパスは通す", "---\nmetadata:\n  esa-sync: a--b/SKILL.md\n---\n", "a--b/SKILL.md", ""},
		{"上へ出るパス", "---\nmetadata:\n  esa-sync: ../x.md\n---\n", "", "パスの要素"},
		{".md で終わらない", "---\nmetadata:\n  esa-sync: a/run.sh\n---\n", "", ".md で終えて"},
	}
	for _, tc := range cases {
		files, _, _, err := mapSyncFiles("R", []int{1}, []map[string]any{post(1, "R", "題", tc.body)})
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v; want %q を含むエラー", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || len(files) != 1 || files[0].rel != tc.want || files[0].body != tc.body {
			t.Errorf("%s: files = %+v, err = %v; want %s に本文のまま", tc.name, files, err, tc.want)
		}
	}
}

// 結果の側の注意（issue 013 の red team 2 周目）: skill の front matter があるのに指定が無く SKILL.md 以外へ書く記事。
func TestSyncSkillWarning(t *testing.T) {
	cases := []struct {
		name, body, rel string
		want            bool
	}{
		{"skill で指定の書き損じ", "---\nname: x\ndescription: d\nmetadata:\n  esa-sync:a/SKILL.md\n---\n", "月次監査.md", true},
		{"SKILL.md へ書くなら出さない", "---\nname: x\ndescription: d\n---\n", "x/SKILL.md", false},
		{"カテゴリ直下の SKILL.md には出す", "---\nname: x\ndescription: d\n---\n", "SKILL.md", true},
		{"小文字の skill.md には出す", "---\nname: x\ndescription: d\n---\n", "x/skill.md", true},
		{"name が無くても description があれば出す", "---\ndescription: d\n---\n", "x.md", true},
		{"name だけ", "---\nname: x\n---\n", "x.md", false},
		{"front matter が無い", "body\n", "x.md", false},
		{"壊れた YAML", "---\nname: [\n---\n", "x.md", false},
	}
	for _, tc := range cases {
		if got := syncSkillWarning(tc.body, tc.rel) != ""; got != tc.want {
			t.Errorf("%s: syncSkillWarning = %v; want %v", tc.name, got, tc.want)
		}
	}
}

// 注意が dry-run に出て、変更なしになった後も出続けること。
func TestSyncPrintsSkillWarning(t *testing.T) {
	body := "---\nname: nr\ndescription: d\nmetadata:\n  esa-sync:nr/SKILL.md\n---\nbody\n"
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{1: post(1, "Users/me/skills", "月次監査", body)}}
	srv := f.serve(t)
	tg := syncTarget{Name: "skills", Category: "Users/me/skills", Dir: t.TempDir()}
	for _, apply := range []bool{false, true, false} {
		_, out, err := runSync(t, srv, tg, apply)
		if err != nil {
			t.Fatalf("apply=%v: %v\n%s", apply, err, out)
		}
		if !strings.Contains(out, "注意: 月次監査.md（esa #1）: skill の front matter") {
			t.Errorf("apply=%v: 注意が出ていない:\n%s", apply, out)
		}
	}
}

// 指定のある記事には注意を出さない（agents などに自分のパスを指定して注意を消す使い方。issue 013 の red team 3 周目）。
func TestSyncSkillWarningNotForDirective(t *testing.T) {
	body := "---\nname: r\ndescription: d\nmetadata:\n  esa-sync: agents/reviewer.md\n---\n"
	files, _, _, err := mapSyncFiles("R", []int{1}, []map[string]any{post(1, "R", "reviewer", body)})
	if err != nil || len(files) != 1 || files[0].rel != "agents/reviewer.md" || len(files[0].warns) != 0 {
		t.Errorf("files = %+v, err = %v; want agents/reviewer.md に注意なし", files, err)
	}
}
