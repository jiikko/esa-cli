package main

import (
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSlackLinkEscapesTitleAndRestoresEsaEntities(t *testing.T) {
	post := map[string]any{
		"name": `a&#47;b <x> & "q"`,
		"url":  "https://t.esa.io/posts/1?a=1&b=2",
		"wip":  true,
	}
	plain, htmlDoc, err := slackLink(post)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[WIP] a/b <x> & "q" https://t.esa.io/posts/1?a=1&b=2`; plain != want {
		t.Errorf("plain = %q, want %q", plain, want)
	}
	want := `<meta charset="utf-8"><a href="https://t.esa.io/posts/1?a=1&amp;b=2">[WIP] a/b &lt;x&gt; &amp; &#34;q&#34;</a>`
	if htmlDoc != want {
		t.Errorf("html = %q, want %q", htmlDoc, want)
	}
}

func TestSlackLinkRejectsMissingFields(t *testing.T) {
	for _, post := range []map[string]any{
		{"url": "https://t.esa.io/posts/1"},
		{"name": "x"},
	} {
		if _, _, err := slackLink(post); err == nil {
			t.Errorf("%v: エラーにならない", post)
		}
	}
}

// タイトルが AppleScript のソースに入らず、argv と 16 進リテラルだけで渡ることを固定する。
func TestCopyToClipboardDoesNotEmbedTextInScript(t *testing.T) {
	var got []string
	orig := runOsascript
	t.Cleanup(func() { runOsascript = orig })
	runOsascript = func(args ...string) ([]byte, error) { got = args; return nil, nil }

	plain := `x" & (do shell script "echo pwned") & "`
	htmlDoc := `<a href="u">` + plain + `</a>`
	if err := copyToClipboard(plain, htmlDoc); err != nil {
		t.Fatal(err)
	}
	sep := -1
	for i, a := range got {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep != len(got)-2 || got[sep+1] != plain {
		t.Fatalf("テキストが argv の末尾で渡っていない: %q", got)
	}
	script := strings.Join(got[:sep], "\n")
	if strings.Contains(script, "pwned") {
		t.Fatalf("タイトルがスクリプトに埋め込まれている: %q", script)
	}
	start := strings.Index(script, "«data HTML")
	end := strings.Index(script, "»,")
	if start < 0 || end < start {
		t.Fatalf("HTML の data リテラルが無い: %q", script)
	}
	decoded, err := hex.DecodeString(script[start+len("«data HTML") : end])
	if err != nil || string(decoded) != htmlDoc {
		t.Fatalf("HTML の復元 = %q (%v), want %q", decoded, err, htmlDoc)
	}
}

func TestCopyToClipboardReportsFailure(t *testing.T) {
	orig := runOsascript
	t.Cleanup(func() { runOsascript = orig })
	runOsascript = func(args ...string) ([]byte, error) { return []byte("boom"), errors.New("exit status 1") }
	if err := copyToClipboard("p", "h"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("失敗が伝わらない: %v", err)
	}
}

func TestParseTargetArgsAcceptsFlagsAfterNumber(t *testing.T) {
	cases := []struct {
		args           []string
		want           int
		copy, comments bool
	}{
		{[]string{"-copy", "12"}, 12, true, false},
		{[]string{"12", "-copy"}, 12, true, false},
		{[]string{"12", "-comments"}, 12, false, true},
		{[]string{"https://t.esa.io/posts/34#comment-5", "-copy"}, 34, true, false},
		{[]string{"https://t.esa.io/posts/56/?x=1"}, 56, false, false},
	}
	for _, c := range cases {
		fs := newFlagSet("meta")
		var cp, cm bool
		fs.BoolVar(&cp, "copy", false, "")
		fs.BoolVar(&cm, "comments", false, "")
		n, done, err := parseTargetArgs(fs, "", c.args, "meta")
		if err != nil || done {
			t.Errorf("%q: err=%v done=%v", c.args, err, done)
			continue
		}
		if n != c.want || cp != c.copy || cm != c.comments {
			t.Errorf("%q: n=%d copy=%v comments=%v, want %d %v %v", c.args, n, cp, cm, c.want, c.copy, c.comments)
		}
	}
}

func TestParseTargetArgsRejectsSecondTarget(t *testing.T) {
	fs := newFlagSet("meta")
	_, _, err := parseTargetArgs(fs, "", []string{"1", "2"}, "meta")
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("使い方の誤りにならない: %v", err)
	}
}

// --help を受けたら done=true を返し、呼び出し側が処理を止められることを固定する
// （done を落とすと help を出した後に number=0 で通信へ進む）。
func TestParseTargetArgsReportsHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"12", "-h"}} {
		fs := newFlagSet("meta")
		fs.SetOutput(io.Discard)
		_, done, err := parseTargetArgs(fs, "", args, "meta")
		if err != nil || !done {
			t.Errorf("%q: done=%v err=%v, want done=true", args, done, err)
		}
	}
}

func TestMetaCopyRejectsJSONAndComments(t *testing.T) {
	blockRealBackends(t) // 退行して通信へ進んでも実環境（Keychain・実 esa）に届かせない
	for _, args := range [][]string{{"-copy", "-json", "1"}, {"1", "-copy", "-comments"}, {"-copy-title", "-json", "1"}, {"-copy", "-copy-title", "1"}} {
		var ue *usageError
		if err := cmdMeta(args); !errors.As(err, &ue) {
			t.Errorf("%q: 使い方の誤りにならない: %v", args, err)
		}
	}
}

// -copy の既定（紹介カード）: タイトルは HTML ではリンク、テキストでは最終行に URL。
// カテゴリ・タグ・本文はエスケープし、esa のエスケープ（&#47; 等）は復元する。
func TestSlackCardLayoutAndEscaping(t *testing.T) {
	post := map[string]any{
		"name": "a&#47;b <x>", "url": "https://t.esa.io/posts/1?a=1&b=2", "wip": true,
		"category":   "dev&#47;Tips",
		"created_by": map[string]any{"screen_name": "koji"},
		"updated_at": "2026-09-28T10:13:30+09:00",
		"tags":       []any{"go", "<tui>"},
		"body_md":    "# 見出し\n\n```\nsecret code\n```\n本文の[リンク](https://x)と**強調**。 x<y&z\n| 表 |\n",
	}
	plain, htmlDoc, err := slackCard(post)
	if err != nil {
		t.Fatal(err)
	}
	wantPlain := "📄 [WIP] a/b <x>\ndev/Tips / @koji / 2026-09-28 / #go #<tui>\n> 見出し 本文のリンクと強調。 x<y&z\nhttps://t.esa.io/posts/1?a=1&b=2"
	if plain != wantPlain {
		t.Errorf("plain =\n%s\nwant\n%s", plain, wantPlain)
	}
	wantHTML := `<meta charset="utf-8">📄 <a href="https://t.esa.io/posts/1?a=1&amp;b=2">[WIP] a/b &lt;x&gt;</a><br>` +
		`dev/Tips / @koji / 2026-09-28 / #go #&lt;tui&gt;<br><blockquote>見出し 本文のリンクと強調。 x&lt;y&amp;z</blockquote>`
	if htmlDoc != wantHTML {
		t.Errorf("html =\n%s\nwant\n%s", htmlDoc, wantHTML)
	}
	if _, _, err := slackCard(map[string]any{"name": "x"}); err == nil {
		t.Error("URL が無いのに成功した")
	}
}

// 概要は文字数（rune）で切り、超えたら … を付ける。無い要素（本文・メタ）の行は出さない。
func TestSlackCardTruncatesByRunesAndOmitsEmptyLines(t *testing.T) {
	long := strings.Repeat("あ", excerptRunes+5)
	plain, _, err := slackCard(map[string]any{"name": "t", "url": "https://t.esa.io/posts/2", "body_md": long})
	if err != nil {
		t.Fatal(err)
	}
	if want := "📄 t\n> " + strings.Repeat("あ", excerptRunes) + "…\nhttps://t.esa.io/posts/2"; plain != want {
		t.Errorf("plain = %q, want %q", plain, want)
	}
	plain, _, _ = slackCard(map[string]any{"name": "t", "url": "https://t.esa.io/posts/2"})
	if want := "📄 t\nhttps://t.esa.io/posts/2"; plain != want {
		t.Errorf("本文もメタも無いときの形 = %q, want %q", plain, want)
	}
}
