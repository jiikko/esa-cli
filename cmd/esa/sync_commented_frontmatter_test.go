package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// esa のプレビューに出さないよう <!-- --> で包んだ front matter の metadata.esa-sync（issue 016）。
// 実例の本文（esa の記事 README。skip の後ろに空白、前後に空行）をそのまま使う。
const commentedSkipBody = "\n<!--\n---\nmetadata:\n  esa-sync: skip \n---\n-->\n\nubiregi向けに作ったclaude codeを管理するカテゴリです。\n\n以上。\n"

func TestFindSyncDirectiveCommentedFrontMatter(t *testing.T) {
	cases := []struct {
		name, body string
		spec       string
		found      bool
		wantErr    string // 空ならエラーなし
	}{
		{"実例の skip", commentedSkipBody, "skip", true, ""},
		// パスは書けない（書き出したファイルでは front matter にならず、壊れた SKILL.md を作る。red team P2-2）
		{"パスの指定", "<!--\n---\nname: x\ndescription: y\nmetadata:\n  esa-sync: x/SKILL.md\n---\n-->\nbody\n", "", false, "書けるのは skip だけ"},
		{"<!-- と --- の間に空行", "<!--\n\n---\nmetadata:\n  esa-sync: skip\n---\n-->\nbody\n", "skip", true, ""},
		{"行の前後の空白と BOM", "\ufeff<!-- \n --- \nmetadata:\n  esa-sync: skip\n---  \n  -->\nbody\n", "skip", true, ""},
		{"閉じの --- と --> の間に空行", "<!--\n---\nmetadata:\n  esa-sync: skip\n---\n\n-->\nbody\n", "skip", true, ""},
		{"ふつうのコメント（--- で始まらない）", "<!--\nメモ: esa-sync: skip\n-->\nbody\n", "", false, ""},
		{"1 行目がコメントの文", "<!-- メモ\n---\nmetadata:\n  esa-sync: skip\n---\n-->\nbody\n", "", false, ""},
		{"指定の無い包んだ front matter", "<!--\n---\nname: a\n---\n-->\nbody\n", "", false, ""},
		// 書き損じは front matter と同じくエラーにする
		{"metadata の外", "<!--\n---\nesa-sync: skip\n---\n-->\nbody\n", "", false, "コメントで包んだ front matter の esa-sync"},
		{"下線のキー", "<!--\n---\nmetadata:\n  esa_sync: skip\n---\n-->\nbody\n", "", false, "コメントで包んだ front matter の metadata.esa_sync"},
		{"文字列でない値", "<!--\n---\nmetadata:\n  esa-sync: false\n---\n-->\nbody\n", "", false, "コメントで包んだ front matter の metadata.esa-sync は"},
		{"YAML が読めない", "<!--\n---\nmetadata: esa-sync: skip\n---\n-->\nbody\n", "", false, "コメントで包んだ front matter の YAML が読めません"},
		{"閉じの --- が無い", "<!--\n---\nmetadata:\n  esa-sync: skip\n-->\nbody\n", "", false, "閉じの --- か --> がありません"},
		{"--> が無い", "<!--\n---\nmetadata:\n  esa-sync: skip\n---\nbody\n", "", false, "閉じの --- か --> がありません"},
		{"末尾のコメントの指定と両方", "<!--\n---\nmetadata:\n  esa-sync: skip\n---\n-->\nbody\n<!-- esa-sync: a.md -->\n", "", false, "2 つにあります"},
		// 両方 skip なら意味が同じなので止めない（v0.4.0 の注意に従って末尾に足した記事。red team P2-1）
		{"末尾のコメントも skip", "<!--\n---\nmetadata:\n  esa-sync: skip\n---\n-->\nbody\n<!-- esa-sync: skip -->\n", "skip", true, ""},
		{"本物の front matter と末尾が両方 skip", "---\nmetadata:\n  esa-sync: skip\n---\nbody\n<!-- esa-sync: skip -->\n", "skip", true, ""},
		// 閉じの無いときは最初の空行までしか見ない（front matter と揃える。red team P3）
		{"閉じが無く、空行の後ろに説明の文", "<!--\n---\nname: a\n\n本文。esa-sync: foo.md の形で書く\n以上。\n", "", false, ""},
	}
	for _, tc := range cases {
		spec, rest, found, err := findSyncDirective(normalizeSyncBody(tc.body))
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
		if spec != tc.spec || found != tc.found {
			t.Errorf("%s: = %q, %v; want %q, %v", tc.name, spec, found, tc.spec, tc.found)
		}
		if found && tc.name != "末尾のコメントも skip" && tc.name != "本物の front matter と末尾が両方 skip" && rest != normalizeSyncBody(tc.body) { // 包んだ front matter は本文のまま書き出す（取り除かない）
			t.Errorf("%s: rest が本文と違う: %q", tc.name, rest)
		}
	}
}

// 偽の esa から: 実例の記事は書き出されず、repo の README.md は変わらず、注意も出ない。
func TestSyncSkipsCommentedFrontMatter(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "Users/me/local", "README", commentedSkipBody),
	}}
	srv := f.serve(t)
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("repo の README\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg := syncTarget{Name: "local", Category: "Users/me/local", Dir: dir}
	n, out, err := runSync(t, srv, tg, true)
	if err != nil || n != 0 {
		t.Fatalf("apply: n=%d err=%v\n%s", n, err, out)
	}
	if !strings.Contains(out, "  - Users/me/local/README  書き出さない esa #1") || strings.Contains(out, "注意:") {
		t.Errorf("書き出さない行が無いか、注意が出ている:\n%s", out)
	}
	if got := readFile(t, readme); got != "repo の README\n" {
		t.Errorf("README.md が書き換えられた: %q", got)
	}
}
