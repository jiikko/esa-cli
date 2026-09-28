package main

import (
	"encoding/hex"
	"fmt"
	"html"
	"os/exec"
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
	// esa は name 内の "/" を &#47;、"#" を &#35; にエスケープして返すため復元する。
	title := html.UnescapeString(name)
	if wip, _ := post["wip"].(bool); wip {
		title = "[WIP] " + title
	}
	plain = title + " " + u
	// charset が無いと、貼り先によっては日本語を Latin-1 として読んで化ける。
	htmlDoc = `<meta charset="utf-8"><a href="` + html.EscapeString(u) + `">` + html.EscapeString(title) + `</a>`
	return plain, htmlDoc, nil
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
