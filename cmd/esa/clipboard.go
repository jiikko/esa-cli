package main

import (
	"encoding/hex"
	"fmt"
	"html"
	"os/exec"
	"regexp"
	"strings"
)

// slackLink は記事から、Slack に貼るとタイトルの文字がリンクになるクリップボードの中身を作る。
// plain は HTML を受け取らない貼り先向け（「タイトル URL」）。
func slackLink(post map[string]any) (plain, htmlDoc string, err error) {
	name, _ := post["name"].(string)
	u, _ := post["url"].(string)
	if name == "" || u == "" {
		return "", "", fmt.Errorf("記事のタイトルか URL が取得できませんでした")
	}
	// esa は name 内の "/" を &#47;、"#" を &#35; にエスケープして返すため復元する（postTitle）。
	title := postTitle(post)
	plain = title + " " + u
	// charset が無いと、貼り先によっては日本語を Latin-1 として読んで化ける。
	htmlDoc = `<meta charset="utf-8"><a href="` + html.EscapeString(u) + `">` + html.EscapeString(title) + `</a>`
	return plain, htmlDoc, nil
}

// excerptRunes は -copy の概要に載せる本文の長さ（文字数）。
const excerptRunes = 120

// slackCard は -copy の既定の形（おすすめ記事の紹介向けのカード）を作る。
//
//	📄 タイトル（HTML ではリンク）
//	カテゴリ / @作成者 / 更新日 / #タグ
//	> 本文の冒頭（Markdown の記法を落として excerptRunes 文字まで）
//	URL（テキスト版だけ。HTML ではタイトルがリンクなので載せない）
func slackCard(post map[string]any) (plain, htmlDoc string, err error) {
	_, _, err = slackLink(post) // タイトル・URL の検証と復元は同じ規則で行う
	if err != nil {
		return "", "", err
	}
	title := postTitle(post)
	u, _ := post["url"].(string)
	meta := cardMeta(post)
	body, _ := post["body_md"].(string)
	ex := excerpt(body, excerptRunes)

	lines := []string{"📄 " + title}
	hlines := []string{"📄 <a href=\"" + html.EscapeString(u) + "\">" + html.EscapeString(title) + "</a>"}
	if meta != "" {
		lines = append(lines, meta)
		hlines = append(hlines, html.EscapeString(meta))
	}
	if ex != "" {
		lines = append(lines, "> "+ex)
		hlines = append(hlines, "<blockquote>"+html.EscapeString(ex)+"</blockquote>")
	}
	lines = append(lines, u)
	return strings.Join(lines, "\n"), `<meta charset="utf-8">` + strings.Join(hlines, "<br>"), nil
}

// postTitle は表示用のタイトル（esa のエスケープを復元し、WIP なら [WIP] を付ける）。
func postTitle(post map[string]any) string {
	name, _ := post["name"].(string)
	title := html.UnescapeString(name)
	if wip, _ := post["wip"].(bool); wip {
		title = "[WIP] " + title
	}
	return title
}

// cardMeta は「カテゴリ / @作成者 / 更新日 / #タグ」の行（無い要素は省く）。
func cardMeta(post map[string]any) string {
	var parts []string
	if c, _ := post["category"].(string); c != "" {
		parts = append(parts, html.UnescapeString(c))
	}
	if by, ok := post["created_by"].(map[string]any); ok {
		if sn, _ := by["screen_name"].(string); sn != "" {
			parts = append(parts, "@"+sn)
		}
	}
	if d, _ := post["updated_at"].(string); len(d) >= 10 {
		parts = append(parts, d[:10])
	}
	if tags, ok := post["tags"].([]any); ok && len(tags) > 0 {
		ts := make([]string, 0, len(tags))
		for _, t := range tags {
			if s, ok := t.(string); ok && s != "" {
				ts = append(ts, "#"+s)
			}
		}
		if len(ts) > 0 {
			parts = append(parts, strings.Join(ts, " "))
		}
	}
	return strings.Join(parts, " / ")
}

var (
	mdImage    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdLineHead = regexp.MustCompile(`^(#+\s*|[-*+]\s+|\d+\.\s+|>\s*)`)
	mdEmphasis = regexp.MustCompile("[*_`~]")
)

// excerpt は Markdown の本文から、記法を落とした冒頭を n 文字（rune）まで返す（超えたら … を付ける）。
// コードブロック・表・HTML・区切り線は概要にならないので飛ばす。
func excerpt(md string, n int) string {
	var words []string
	inCode := false
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") {
			inCode = !inCode
			continue
		}
		if inCode || l == "" || strings.HasPrefix(l, "|") || strings.HasPrefix(l, "<") || strings.HasPrefix(l, "---") {
			continue
		}
		l = mdLineHead.ReplaceAllString(l, "")
		l = mdImage.ReplaceAllString(l, "")
		l = mdLink.ReplaceAllString(l, "$1")
		l = strings.TrimSpace(mdEmphasis.ReplaceAllString(l, ""))
		if l != "" {
			words = append(words, l)
		}
	}
	r := []rune(strings.Join(words, " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// runOsascript はテストで差し替える seam。
var runOsascript = func(args ...string) ([]byte, error) {
	return exec.Command("/usr/bin/osascript", args...).CombinedOutput()
}

// copyToClipboard は HTML とテキストの 2 形式を同時にクリップボードへ入れる。
//
// pbcopy はテキストしか入れられないため osascript を使う（NSPasteboard を直接呼ぶと cgo が要り、
// CGO_ENABLED=0 でビルドできなくなる）。
// 🚨 タイトルを AppleScript のソースに埋め込まない。引用符で構文が壊れ、記事タイトル次第で任意の
// スクリプトとして実行されうる。HTML は 16 進の data リテラル（[0-9A-F] のみ）、テキストは argv で渡す。
func copyToClipboard(plain, htmlDoc string) error {
	h := strings.ToUpper(hex.EncodeToString([]byte(htmlDoc)))
	out, err := runOsascript(
		"-e", "on run argv",
		"-e", "set the clipboard to {«class HTML»:«data HTML"+h+"», string:(item 1 of argv)}",
		"-e", "end run",
		"--", plain,
	)
	if err != nil {
		return fmt.Errorf("クリップボードへのコピーに失敗しました: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
