package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 書き出し先の指定の skip（issue 015）: コメントの形の見つけ方と、崩れた skip・説明の文の扱い。
func TestExtractSyncDirectiveSkip(t *testing.T) {
	cases := []struct {
		name, body string
		spec, rest string
		found      bool
		wantErr    bool
	}{
		{"skip", "TODO:\n<!-- esa-sync: skip -->\n", "skip", "TODO:\n", true, false},
		{"本文が skip だけ", "<!-- esa-sync: skip -->\n", "skip", "", true, false},
		// 崩れた skip は黙って記事名の規則に戻さない
		{"空白なし", "text\n<!-- esa-sync:skip -->\n", "", "", false, true},
		{"大文字のキー", "text\n<!-- ESA-SYNC: skip -->\n", "", "", false, true},
		{"下線", "text\n<!-- esa_sync: skip -->\n", "", "", false, true},
		{"全角のコロン", "text\n<!-- esa-sync： skip -->\n", "", "", false, true},
		{"コメントで包み忘れ", "text\nesa-sync: skip\n", "", "", false, true},
		{"HTML エンティティ", "text\n&lt;!-- esa-sync: skip --&gt;\n", "", "", false, true},
		{"字下げ", "text\n    <!-- esa-sync: skip -->\n", "", "", false, true},
		{"引用", "text\n> <!-- esa-sync: skip -->\n", "", "", false, true},
		{"コード記法", "text\n`esa-sync: skip`\n", "", "", false, true},
		{"先頭に書いた skip", "<!-- esa-sync: skip -->\ntext\n", "", "", false, true},
		// 最後の行が skip なら、skip らしい別の行とは意味が食い違わないので止めない（red team: 使い方の例の直後に本物の指定を書く記事）
		{"skip が 2 つ", "text\n<!-- esa-sync: skip -->\n<!-- esa-sync: skip -->\n", "skip", "text\n<!-- esa-sync: skip -->\n", true, false},
		{"例の直後の skip", "次の行を書く:\n\n`<!-- esa-sync: skip -->`\n\n<!-- esa-sync: skip -->\n", "skip", "次の行を書く:\n\n`<!-- esa-sync: skip -->`\n", true, false},
		{".md の後ろに skip らしい行は食い違う", "text\n<!-- esa-sync:skip -->\n<!-- esa-sync: a.md -->\n", "", "", false, true},
		// 本文の途中の厳密な skip は、最後の行が skip でなければ止める（コードブロックの中は見ない）
		{"本文の途中の skip", "# タイトル\n<!-- esa-sync: skip -->\n本文\n", "", "", false, true},
		{"skip の後ろに別のコメント", "text\n<!-- esa-sync: skip -->\n<!-- TODO: x -->\n", "", "", false, true},
		{"最後が .md で途中に skip", "text\n<!-- esa-sync: skip -->\nmore\n<!-- esa-sync: a.md -->\n", "", "", false, true},
		{"コードブロックの中の skip は見ない", "例:\n```\n<!-- esa-sync: skip -->\n```\n本文\n", "", "例:\n```\n<!-- esa-sync: skip -->\n```\n本文\n", false, false},
		{"番号付きの項目のコメント", "text\n1. <!-- esa-sync: skip -->\n", "", "", false, true},
		// red team 2 周目: フェンスは記号の種類と長さで閉じを決める（CommonMark）
		{"入れ子のフェンスの中の skip", "例:\n````md\n```md\n<!-- esa-sync: skip -->\n```\n````\n本文\n", "", "例:\n````md\n```md\n<!-- esa-sync: skip -->\n```\n````\n本文\n", false, false},
		{"短いフェンスは閉じない", "例:\n````\n```\n<!-- esa-sync: skip -->\n````\n本文\n", "", "例:\n````\n```\n<!-- esa-sync: skip -->\n````\n本文\n", false, false},
		{"~~~ の中の ```", "例:\n~~~\n```\n<!-- esa-sync: skip -->\n```\n~~~\n本文\n", "", "例:\n~~~\n```\n<!-- esa-sync: skip -->\n```\n~~~\n本文\n", false, false},
		{"info 付きの行は閉じない", "例:\n```\n```go\n<!-- esa-sync: skip -->\n```\n本文\n", "", "例:\n```\n```go\n<!-- esa-sync: skip -->\n```\n本文\n", false, false},
		{"字下げ 4 のフェンスはフェンスでない", "例:\n    ```\n<!-- esa-sync: skip -->\n本文\n", "", "", false, true},
		// 説明の文・見出し・別の語は止めない（反証レビュー P1）
		{"説明の文", "text\n書き出さないなら esa-sync: skip と書くと、その記事は書き出されません。\n", "",
			"text\n書き出さないなら esa-sync: skip と書くと、その記事は書き出されません。\n", false, false},
		{"見出し", "# esa-sync: skip を使う\ntext\n", "", "# esa-sync: skip を使う\ntext\n", false, false},
		{"skipping", "text\n- esa sync: skipping\n", "", "text\n- esa sync: skipping\n", false, false},
		{"箇条書きの説明", "使い方:\n- esa-sync: skip\n", "", "使い方:\n- esa-sync: skip\n", false, false},
		{"箇条書きのコード", "使い方:\n- `esa-sync: skip`\n", "", "使い方:\n- `esa-sync: skip`\n", false, false},
	}
	for _, tc := range cases {
		spec, rest, found, err := extractSyncDirective(tc.body)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v; wantErr %v", tc.name, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if spec != tc.spec || rest != tc.rest || found != tc.found {
			t.Errorf("%s: extractSyncDirective(%q) = %q, %q, %v; want %q, %q, %v", tc.name, tc.body, spec, rest, found, tc.spec, tc.rest, tc.found)
		}
	}
}

// front matter の metadata.esa-sync: skip と、その崩れ。
func TestFindSyncDirectiveFrontMatterSkip(t *testing.T) {
	cases := []struct {
		name, body string
		spec       string
		found      bool
		wantErr    bool
	}{
		{"metadata の skip", "---\nname: a\nmetadata:\n  esa-sync: skip\n---\nbody\n", "skip", true, false},
		{"最上位の skip", "---\nname: a\nesa-sync: skip\n---\nbody\n", "", false, true},
		{"下線のキー", "---\nname: a\nmetadata:\n  esa_sync: skip\n---\nbody\n", "", false, true},
		{"大文字の値は最上位でも拾う", "---\nESA-SYNC: SKIP\n---\nbody\n", "", false, true},
		{"閉じの無い front matter", "---\nname: a\nmetadata:\n  esa-sync: skip\n\nbody\n", "", false, true},
		{"YAML が読めない", "---\nname: [a\nmetadata:\n  esa-sync: skip\n---\nbody\n", "", false, true},
		// red team P1: 1 行にまとめた metadata と閉じ忘れのフロー形式（YAML として読めない）
		{"1 行にまとめた metadata", "---\nname: x\ndescription: y\nmetadata: esa-sync: skip\n---\nbody\n", "", false, true},
		{"閉じ忘れのフロー形式", "---\nname: x\ndescription: y\nmetadata: {esa-sync: skip\n---\nbody\n", "", false, true},
		{"読めるフロー形式は skip として効く", "---\nname: x\nmetadata: {esa-sync: skip}\n---\nbody\n", "skip", true, false},
		{"読める YAML の説明の値は拾わない", "---\nname: a\ndescription: \"esa-sync: skip で書き出さない\"\n---\nbody\n", "", false, false},
		// 引用符が無いと YAML として読めない（mapping values）。読めない front matter の行の検出でも、文の中の skip は拾わない
		{"読めない YAML の中の説明の文は拾わない", "---\nname: a\ndescription: esa-sync: skip で書き出さない\n---\nbody\n", "", false, false},
	}
	for _, tc := range cases {
		spec, _, found, err := findSyncDirective(tc.body)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v; wantErr %v", tc.name, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && (spec != tc.spec || found != tc.found) {
			t.Errorf("%s: = %q, %v; want %q, %v", tc.name, spec, found, tc.spec, tc.found)
		}
	}
}

// skip の記事はファイルにならず、パス・衝突の検査もしない。対象の外の記事は skip でも今どおり除く。
func TestMapSyncFilesSkip(t *testing.T) {
	numbers := []int{1, 2, 3, 4, 5}
	posts := []map[string]any{
		post(1, "R", "README", "TODO:\n<!-- esa-sync: skip -->\n"),
		post(2, "R", "説明/索引", "---\nmetadata:\n  esa-sync: skip\n---\n"), // 記事名の / は skip なら問題にしない
		post(3, "R", "README", "書く\n"),                                   // skip の記事と同じ記事名でも衝突しない
		post(4, "R2", "x", "<!-- esa-sync: skip -->\n"),                  // 対象の外
		post(5, "R/sub", "a", "a\n"),
	}
	files, skipped, excluded, err := mapSyncFiles("R", numbers, posts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.rel)
	}
	if strings.Join(rels, ",") != "README.md,sub/a.md" {
		t.Errorf("files = %v; want [README.md sub/a.md]", rels)
	}
	if len(skipped) != 2 || skipped[0].number != 1 || skipped[1].number != 2 || skipped[1].name != "説明/索引" {
		t.Errorf("skipped = %+v; want #1 と #2", skipped)
	}
	if excluded != 1 {
		t.Errorf("excluded = %d; want 1", excluded)
	}

	// 大文字の SKIP はパスとして読んでエラーにし、skip を案内する
	_, _, _, err = mapSyncFiles("R", []int{1}, []map[string]any{post(1, "R", "a", "a\n<!-- esa-sync: SKIP -->\n")})
	if err == nil || !strings.Contains(err.Error(), "小文字で skip") {
		t.Errorf("SKIP: err = %v; want skip を案内するエラー", err)
	}
}

// 偽の esa から: skip の記事は書き出されず、記事名のパスにあるローカルのファイルは変わらず、出力に必ず出る。
func TestSyncSkipsSkipDirective(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "Users/me/local", "README", "TODO:\n<!-- esa-sync: skip -->\n"),
		2: post(2, "Users/me/local", "note", "note\n"),
	}}
	srv := f.serve(t)
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("repo の README\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg := syncTarget{Name: "local", Category: "Users/me/local", Dir: dir}

	n, out, err := runSync(t, srv, tg, false)
	if err != nil || n != 1 {
		t.Fatalf("dry-run: n=%d err=%v\n%s", n, err, out)
	}
	for _, want := range []string{
		"  - Users/me/local/README  書き出さない esa #1（書き出し先の指定 esa-sync: skip）\n",
		"  記事 1 件: 新規 1 / 変更 0 / 変更なし 0 / 書き出さない 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run に %q が無い:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("skip の記事が README.md の差分として出ている:\n%s", out)
	}

	if _, out, err := runSync(t, srv, tg, true); err != nil || !strings.Contains(out, "書き出さない esa #1") {
		t.Fatalf("apply: err=%v（--apply にも書き出さない記事が出るはず）\n%s", err, out)
	}
	if got := readFile(t, readme); got != "repo の README\n" {
		t.Errorf("README.md が書き換えられた: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "note.md")); got != "note\n" {
		t.Errorf("note.md = %q", got)
	}

	// 書き出さない記事しか差分が無ければ、書き込み（予定）は 0 件（「dry-run です」の案内を出さない）
	n, out, err = runSync(t, srv, tg, false)
	if err != nil || n != 0 || !strings.Contains(out, "/ 書き出さない 1") {
		t.Errorf("2 回目: n=%d err=%v\n%s", n, err, out)
	}
}

// 結果の側の注意（issue 015 の red team）: 書き出す記事の本文に skip の指定らしい行があれば、字面の検出をすり抜けても出力に必ず注意が出る。
func TestSyncSkipIntentWarning(t *testing.T) {
	cases := []struct {
		name, body string
		warn       bool
	}{
		{"番号の後ろの句点で崩れたコメント", "text\n<!--esa-sync: skip。-->\n", true},
		{"metadata の子のコロンの後の空白の抜け", "---\nname: a\nmetadata:\n  esa-sync:skip\n---\nbody\n", true},
		{"箇条書きの説明", "使い方:\n- esa-sync: skip\n", true},
		{"skip を含まない", "text\n<!-- esa-sync: a.md -->\n", false},
		{"skipping", "text\nesa sync: skipping\n", false},
		{"esa と sync が離れた文", "esa の記事は sync しても skip しない\n", false},
		// red team 2 周目: 説明の文には出さない（コロンの直後が skip の行だけ）
		{"説明の文（実行で skip）", "esa sync の実行で skip された記事は出力に出る\n", false},
		{"説明の文（skip は小文字）", "esa-sync を使う。skip は小文字\n", false},
		{"複数行に分けたコメント", "text\n<!-- esa-sync:\nskip -->\n", true},
		// red team 3 周目: コロンの崩れ（注意で拾う）
		{"コロンの抜け", "text\n<!-- esa-sync skip -->\n", true},
		{"イコール", "text\n<!-- esa-sync=skip -->\n", true},
		{"コロンが 2 つ", "text\n<!-- esa-sync:: skip -->\n", true},
		{"コロンの後ろに引用符", "text\n\"esa-sync: 'skip'\"\n", true},
	}
	for _, tc := range cases {
		if got := syncSkipIntentWarning(tc.body) != ""; got != tc.warn {
			t.Errorf("%s: warn = %v; want %v", tc.name, got, tc.warn)
		}
	}

	// 字面の検出をすり抜ける形が、dry-run の出力に注意として出ること
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{1: post(1, "C", "README", "TODO:\n<!--esa-sync: skip。-->\n")}}
	srv := f.serve(t)
	_, out, err := runSync(t, srv, syncTarget{Name: "c", Category: "C", Dir: t.TempDir()}, false)
	if err != nil || !strings.Contains(out, "注意: README.md（esa #1）: 本文に書き出さない指定らしい行") {
		t.Errorf("注意が出ていない: err=%v\n%s", err, out)
	}
}

// 対象の中が全部 skip で隣のカテゴリの記事が検索に混ざっても、「どれもカテゴリが一致しない」の注意を出さない。
func TestSyncAllSkippedWithExcludedHasNoCategoryNote(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "C", "README", "<!-- esa-sync: skip -->\n"),
		2: post(2, "C2", "x", "x\n"),
	}}
	srv := f.serve(t)
	n, out, err := runSync(t, srv, syncTarget{Name: "c", Category: "C", Dir: t.TempDir()}, false)
	if err != nil || n != 0 || !strings.Contains(out, "書き出さない esa #1") {
		t.Fatalf("n=%d err=%v\n%s", n, err, out)
	}
	if strings.Contains(out, "どれもカテゴリが") {
		t.Errorf("全部 skip なのに、カテゴリが一致しない注意が出ている:\n%s", out)
	}
}
