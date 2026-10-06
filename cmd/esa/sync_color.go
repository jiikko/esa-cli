package main

import (
	"os"

	"golang.org/x/term"
)

// syncPalette は esa sync の計画の表示に付ける色。ゼロ値は色なし（パイプ・ファイルへ出すとき）。
type syncPalette struct{ on bool }

const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrDim    = "\x1b[2m"
	sgrRed    = "\x1b[31m"
	sgrGreen  = "\x1b[32m"
	sgrYellow = "\x1b[33m"
	sgrCyan   = "\x1b[36m"
)

// syncPaletteFor は f へ出すときの色を決める。端末で、NO_COLOR が無く、TERM が dumb でないときだけ付ける。
// 判定は isatty で行う（ModeCharDevice だと /dev/null も端末に見える）。
func syncPaletteFor(f *os.File) syncPalette {
	return syncPalette{on: wantColor(term.IsTerminal(int(f.Fd())), os.Getenv)}
}

// syncStdout は esa sync の計画を書く先と、その先に合わせた色を返す。
// 書く先と色を判定する先を 1 か所で決める（別々に書くと、stdout へ書いて stderr で判定するような食い違いが起きる）。
func syncStdout() (*os.File, syncPalette) {
	f := os.Stdout
	return f, syncPaletteFor(f)
}

// wantColor は NO_COLOR（https://no-color.org/: 空でない値なら付けない）と TERM=dumb を見る。
func wantColor(isTerminal bool, getenv func(string) string) bool {
	return isTerminal && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// paint は s を sgr で包む。
//
// 🚨 s は visible() を通した後の文字列であること。visible は本文の ESC を \x{1b} に変えるので、
// ここで足すエスケープだけが端末に効く（逆順にすると、本文が色の範囲を閉じたり端末を操作したりできる）。
func (p syncPalette) paint(sgr, s string) string {
	if !p.on {
		return s
	}
	return sgr + s + sgrReset
}

// diffLine は差分の i 行目（visible 済み）に色を付ける。
//
// 先頭の 2 行は必ず ---/+++ の見出し（unifiedDiff / newFileSummary / largeDiffSummary の形）。
// 見出しを中身の先頭文字で判定しないのは、削除した行の本文が "-- x" なら "--- x" になり見分けられないため。
// 3 行目以降は先頭 1 文字が ' ' / '-' / '+' / '@'（hunk の見出し）のどれかで決まる。
func (p syncPalette) diffLine(i int, line string) string {
	if i < 2 {
		return p.paint(sgrBold, line)
	}
	if line == "" {
		return line
	}
	switch line[0] {
	case '-':
		return p.paint(sgrRed, line)
	case '+':
		return p.paint(sgrGreen, line)
	case '@':
		return p.paint(sgrCyan, line)
	case ' ':
		return line
	}
	return p.paint(sgrDim, line) // largeDiffSummary の「大きいため省略」の説明行
}
