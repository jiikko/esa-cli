package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文の最後の空でない行の書き出し先の指定（issue 010）。見つけ方・取り除き方・崩れた指定の扱い。
func TestExtractSyncDirective(t *testing.T) {
	cases := []struct {
		name, body string
		spec, rest string
		found      bool
		wantErr    bool
	}{
		{"指定なし", "text\n", "", "text\n", false, false},
		{"空の本文", "", "", "", false, false},
		{"指定あり", "text\n<!-- esa-sync: a/SKILL.md -->\n", "a/SKILL.md", "text\n", true, false},
		{"前の空行は全部落とす", "text\n\n\n<!-- esa-sync: a.md -->\n\n", "a.md", "text\n", true, false},
		{"本文が指定だけ", "<!-- esa-sync: a.md -->\n", "a.md", "", true, false},
		{"後ろにゼロ幅だけの行", "text\n<!-- esa-sync: a.md -->\n​\n", "a.md", "text\n", true, false},
		{"本文の途中の同じ形は触らない", "text\n<!-- esa-sync: a.md -->\nmore\n", "", "text\n<!-- esa-sync: a.md -->\nmore\n", false, false},
		{"コードブロックの中は触らない", "```\n<!-- esa-sync: a.md -->\n```\n", "", "```\n<!-- esa-sync: a.md -->\n```\n", false, false},
		{"全角のコロン", "text\n<!-- esa-sync： a.md -->\n", "", "", false, true},
		{"大文字", "text\n<!-- ESA-SYNC: a.md -->\n", "", "", false, true},
		{"空白なし", "text\n<!--esa-sync: a.md -->\n", "", "", false, true},
		{"下線", "text\n<!-- esa_sync: a.md -->\n", "", "", false, true},
		{"先頭にゼロ幅", "text\n​<!-- esa-sync: a.md -->\n", "", "", false, true},
		{"引用", "text\n> <!-- esa-sync: a.md -->\n", "", "", false, true},
		{"字下げ（コード例）", "text\n    <!-- esa-sync: a.md -->\n", "", "", false, true},
		{"閉じ忘れ", "text\n<!-- esa-sync: a.md\n", "", "", false, true},
		{"指定が 2 つ", "text\n<!-- esa-sync: a.md -->\n<!-- esa-sync: b.md -->\n", "", "", false, true},
		{"指定が 2 つ（間に空行）", "<!-- esa-sync: a.md -->\n\n<!-- esa-sync: b.md -->\n", "", "", false, true},
		// 実装への red team で見つかった見逃し
		{"区切りが空白", "text\n<!-- esa sync: a.md -->\n", "", "", false, true},
		{"Markdown のエスケープ", "text\n<!-- esa\\-sync: a.md -->\n", "", "", false, true},
		{"U+2010 のハイフン", "text\n<!-- esa\u2010sync: a.md -->\n", "", "", false, true},
		{"先頭に書いた指定", "<!-- esa-sync: s/SKILL.md -->\n---\nname: s\n---\n", "", "", false, true},
		{"最後の行が esa-sync を含む説明の文（コメントでない）", "text\nuse esa-sync\n", "", "text\nuse esa-sync\n", false, false},
		{"最後の行が esa-sync の URL", "text\nhttps://example.com/esa-sync を参照\n", "", "text\nhttps://example.com/esa-sync を参照\n", false, false},
		// 2 周目: コロンを軸にした判定
		{"em dash のコメント", "text\n<!\u2014 esa-sync: a.md \u2014>\n", "", "", false, true},
		{"コメントで包み忘れ", "text\nesa-sync: a.md\n", "", "", false, true},
		{"HTML エンティティ", "text\n&lt;!-- esa-sync: a.md --&gt;\n", "", "", false, true},
		{"普通のコメント（コロンなし）", "text\n<!-- TODO: esa sync で同期する -->\n", "", "text\n<!-- TODO: esa sync で同期する -->\n", false, false},
		{"語の途中の esa（Mesa）", "text\n<!-- Mesa synchronization: notes -->\n", "", "text\n<!-- Mesa synchronization: notes -->\n", false, false},
		{"先頭の行頭に空白", " <!-- esa-sync: a.md -->\n---\nname: s\n---\n", "", "", false, true},
		{"先頭に BOM", "\ufeff<!-- esa-sync: a.md -->\n---\n", "", "", false, true},
		{"先頭が全角のコロン", "<!-- esa-sync： a.md -->\n---\n", "", "", false, true},
		{"直前の空白だけの行も落とす", "text\n   \n\u200b\n<!-- esa-sync: a.md -->\n", "a.md", "text\n", true, false},
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

// 記事 → 書き出し先の対応に、本文の指定が入ったときの規則（issue 010）。
func TestMapSyncFilesWithDirective(t *testing.T) {
	const root = "R"
	type in struct {
		category, name, body string
	}
	cases := []struct {
		name    string
		posts   []in
		want    map[string]string // rel → 書き出す本文
		wantErr string            // エラー文に含まれるべき文字列（空ならエラーなし）
		notErr  string            // エラー文に含まれてはいけない文字列
	}{
		{
			name:  "指定のある記事は記事名を使わず、指定の行を取り除く",
			posts: []in{{"R", "月次 New Relic 監査: 速度(ubiregi-server編)", "---\nname: x\n---\nbody\n<!-- esa-sync: nr/SKILL.md -->\n"}},
			want:  map[string]string{"nr/SKILL.md": "---\nname: x\n---\nbody\n"},
		},
		{
			name:  "指定の無い記事は今の規則のまま",
			posts: []in{{"R/a", "SKILL", "body\n"}},
			want:  map[string]string{"a/SKILL.md": "body\n"},
		},
		{
			name:  "記事名に / があっても、指定があれば通る",
			posts: []in{{"R", "障害/インシデント対応", "body\n<!-- esa-sync: incident.md -->\n"}},
			want:  map[string]string{"incident.md": "body\n"},
		},
		{
			name:    "記事名に / があり、指定が無ければ今どおりエラー",
			posts:   []in{{"R", "a/b", "body\n"}},
			wantErr: "/ が含まれています",
		},
		{
			name:  "下位カテゴリの記事の指定は、そのカテゴリのディレクトリからの相対",
			posts: []in{{"R/team", "何か", "body\n<!-- esa-sync: bar/SKILL.md -->\n"}},
			want:  map[string]string{"team/bar/SKILL.md": "body\n"},
		},
		{
			name:    "指定で上へ出られない",
			posts:   []in{{"R/team", "何か", "body\n<!-- esa-sync: ../bar/SKILL.md -->\n"}},
			wantErr: "パスの要素",
		},
		{name: "絶対パス", posts: []in{{"R", "x", "b\n<!-- esa-sync: /etc/a.md -->\n"}}, wantErr: "相対パス"},
		{name: "バックスラッシュ", posts: []in{{"R", "x", "b\n<!-- esa-sync: a\\b.md -->\n"}}, wantErr: "相対パス"},
		{name: ".md で終わらない", posts: []in{{"R", "x", "b\n<!-- esa-sync: a/run.sh -->\n"}}, wantErr: ".md で終えて"},
		{name: "-- を含む", posts: []in{{"R", "x", "b\n<!-- esa-sync: a.md --> x.md -->\n"}}, wantErr: "-- は使えません"},
		{name: "空の要素", posts: []in{{"R", "x", "b\n<!-- esa-sync: a//b.md -->\n"}}, wantErr: "パスの要素"},
		{name: "先頭の空白", posts: []in{{"R", "x", "b\n<!-- esa-sync:  a.md -->\n"}}, wantErr: "空白で始まって"},
		{name: "NBSP で始まる要素", posts: []in{{"R", "x", "b\n<!-- esa-sync:  a/b.md -->\n"}}, wantErr: "空白で始まって"},
		{name: "末尾の空白（.md の検査が先に当たる）", posts: []in{{"R", "x", "b\n<!-- esa-sync: a.md  -->\n"}}, wantErr: ".md で終えて"},
		{name: "要素の末尾の空白", posts: []in{{"R", "x", "b\n<!-- esa-sync: a /b.md -->\n"}}, wantErr: "空白で終わって"},
		{name: "要素の末尾の全角空白", posts: []in{{"R", "x", "b\n<!-- esa-sync: a\u3000/b.md -->\n"}}, wantErr: "空白で終わって"},
		{name: "見えない文字", posts: []in{{"R", "x", "b\n<!-- esa-sync: a​b.md -->\n"}}, wantErr: "見えない文字"},
		{name: "一時ファイルの接尾辞", posts: []in{{"R", "x", "b\n<!-- esa-sync: a.md" + syncTmpSuffix + "/b.md -->\n"}}, wantErr: "パスの要素"},
		{name: "長すぎる", posts: []in{{"R", "x", "b\n<!-- esa-sync: " + strings.Repeat("a", 250) + ".md -->\n"}}, wantErr: "長すぎます"},
		{name: "崩れた指定", posts: []in{{"R", "x", "b\n<!-- esa-sync： a.md -->\n"}}, wantErr: "形になっていません"},
		{
			name: "指定どうしの衝突（大文字小文字違い）",
			posts: []in{
				{"R", "one", "b\n<!-- esa-sync: foo/SKILL.md -->\n"},
				{"R", "two", "b\n<!-- esa-sync: FOO/skill.md -->\n"},
			},
			wantErr: "の書き出し先は本文の指定（esa-sync:）による",
		},
		{
			name: "指定と今の規則の衝突",
			posts: []in{
				{"R/foo", "SKILL", "b\n"},
				{"R", "何か", "b\n<!-- esa-sync: foo/SKILL.md -->\n"},
			},
			wantErr: "の書き出し先は本文の指定（esa-sync:）による",
		},
		{
			name: "指定のファイルと、他の記事のディレクトリの衝突（どの記事が指定によるかを出す）",
			posts: []in{
				{"R", "x", "b\n<!-- esa-sync: foo.md -->\n"},
				{"R/foo.md", "y", "b\n"},
			},
			wantErr: "（#1 の書き出し先は本文の指定による）",
		},
		{
			name: "カテゴリから来たディレクトリとの衝突は、指定のせいにしない",
			posts: []in{
				{"R", "foo", "b\n"},
				{"R/foo.md", "y", "b\n<!-- esa-sync: bar.md -->\n"},
			},
			wantErr: "ぶつかります",
			notErr:  "指定による",
		},
		{
			name: "同じファイルの衝突で、指定による記事の番号を出す",
			posts: []in{
				{"R/foo", "SKILL", "b\n"},
				{"R", "何か", "b\n<!-- esa-sync: foo/SKILL.md -->\n"},
			},
			wantErr: "#2 の書き出し先は本文の指定（esa-sync:）による",
		},
		{
			name:  "対象のカテゴリの外は、指定があっても除く",
			posts: []in{{"R2", "x", "b\n<!-- esa-sync: a.md -->\n"}},
			want:  map[string]string{},
		},
	}
	for _, tc := range cases {
		var numbers []int
		var posts []map[string]any
		for i, p := range tc.posts {
			numbers = append(numbers, i+1)
			posts = append(posts, post(i+1, p.category, p.name, p.body))
		}
		files, _, err := mapSyncFiles(root, numbers, posts)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v; want %q を含むエラー", tc.name, err, tc.wantErr)
			} else if tc.notErr != "" && strings.Contains(err.Error(), tc.notErr) {
				t.Errorf("%s: err = %v; %q を含まないはず", tc.name, err, tc.notErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		got := map[string]string{}
		for _, f := range files {
			got[f.rel] = f.body
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: files = %v; want %v", tc.name, got, tc.want)
			continue
		}
		for rel, body := range tc.want {
			if got[rel] != body {
				t.Errorf("%s: %s の本文 = %q; want %q（ファイル: %v）", tc.name, rel, got[rel], body, got)
			}
		}
	}
}

// 指定のある記事が、偽の esa から実際に書き出され、dry-run に「指定による」と出ること。
func TestSyncWritesToDirectivePath(t *testing.T) {
	f := &fakeEsa{perPage: 10, posts: map[int]map[string]any{
		1: post(1, "Users/me/skills", "月次 New Relic 監査: 速度(ubiregi-server編)", "---\nname: nr\n---\nbody\n\n<!-- esa-sync: nr/SKILL.md -->\n"),
		2: post(2, "Users/me/skills/foo", "SKILL", "foo\n"),
	}}
	srv := f.serve(t)
	dir := t.TempDir()
	tg := syncTarget{Name: "skills", Category: "Users/me/skills", Dir: dir}

	n, out, err := runSync(t, srv, tg, false)
	if err != nil || n != 2 {
		t.Fatalf("dry-run: n=%d err=%v\n%s", n, err, out)
	}
	if !strings.Contains(out, "+ nr/SKILL.md  新規 esa #1（書き出し先は本文の指定による）") {
		t.Errorf("dry-run に指定による書き出し先が出ていない:\n%s", out)
	}
	if strings.Contains(out, "foo/SKILL.md  新規 esa #2（書き出し先は本文の指定による）") {
		t.Errorf("指定の無い記事に「指定による」が付いている:\n%s", out)
	}

	if _, out, err := runSync(t, srv, tg, true); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if got := readFile(t, filepath.Join(dir, "nr", "SKILL.md")); got != "---\nname: nr\n---\nbody\n" {
		t.Errorf("nr/SKILL.md = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "foo", "SKILL.md")); got != "foo\n" {
		t.Errorf("foo/SKILL.md = %q", got)
	}
	if n, out, err := runSync(t, srv, tg, false); err != nil || n != 0 {
		t.Errorf("2 回目は変更なしのはず: n=%d err=%v\n%s", n, err, out)
	}
}
