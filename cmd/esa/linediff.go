package main

import (
	"fmt"
	"strings"
)

// maxDiffCells は LCS の表の上限（行数の積）。超えたら差分本文を出さず行数だけを伝える。
const maxDiffCells = 4_000_000

// unifiedDiff は a → b の unified 形式の差分を返す（ctx 行の前後文脈つき）。同じなら空文字。
func unifiedDiff(a, b, labelA, labelB string, ctx int) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	if len(al)*len(bl) > maxDiffCells {
		return largeDiffSummary(al, bl, labelA, labelB)
	}
	ops := diffOps(al, bl)

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", labelA, labelB)
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// 変更のまとまり [start, end) を、間の同一行が 2*ctx 以下なら 1 つの hunk にまとめる。
		start := max(i-ctx, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		aStart, bStart, aN, bN := ops[start].ai, ops[start].bi, 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				aN++
			}
			if op.kind != '-' {
				bN++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(aStart, aN), hunkRange(bStart, bN))
		for _, op := range ops[start:end] {
			sb.WriteByte(op.kind)
			sb.WriteString(op.text)
			sb.WriteByte('\n')
		}
		i = end
	}
	return sb.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

type diffOp struct {
	kind   byte // ' ' / '-' / '+'
	text   string
	ai, bi int // この操作の直前までに消費した a / b の行数
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffOps は LCS に沿って a を b へ変える操作列を返す。
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i], i, j})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]): // 同点なら削除を先に出す（- の後に +）
			ops = append(ops, diffOp{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j], i, j})
			j++
		}
	}
	return ops
}

// largeDiffSummary は LCS を組めない大きさのとき、最初と最後に違う行だけを示す。
//
// 🚨 行数だけを出して省略しない。以前の形では、2000 行を超えるファイルの手編集が dry-run に 1 文字も出ないまま
// --apply で消えた（red team で再現）。先頭と末尾から一致する行を削れば、違う範囲は必ず示せる。
func largeDiffSummary(a, b []string, labelA, labelB string) string {
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", labelA, labelB)
	fmt.Fprintf(&sb, "（大きいため行単位の差分を省略: %d 行 → %d 行。違うのは %d〜%d 行目（ローカル）/ %d〜%d 行目（esa）。先頭と末尾だけを示す）\n",
		len(a), len(b), head+1, len(a)-tail, head+1, len(b)-tail)
	writeElided(&sb, '-', a[head:len(a)-tail], 5)
	writeElided(&sb, '+', b[head:len(b)-tail], 5)
	return sb.String()
}

// syncNewFileShow は新規ファイルの本文を表示する行数の上限（先頭と末尾それぞれ）。
const syncNewFileShow = 150

// newFileSummary は新規ファイルの本文を「空 → 本文」の差分として返す。長ければ先頭と末尾だけにして省いた行数を示す
// （上限が無いと、巨大な新規記事 1 つで他の差分が端末のスクロールバックから押し出される）。
func newFileSummary(body, label string) string {
	lines := splitLines(body)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- （無し）\n+++ %s\n@@ -0,0 +1,%d @@\n", label, len(lines))
	writeElided(&sb, '+', lines, syncNewFileShow)
	return sb.String()
}

// writeElided は lines を sign を付けて書く。2*keep 行を超えたら先頭と末尾の keep 行だけにし、間に省いた行数を書く。
func writeElided(sb *strings.Builder, sign byte, lines []string, keep int) {
	if len(lines) <= 2*keep {
		for _, l := range lines {
			fmt.Fprintf(sb, "%c%s\n", sign, l)
		}
		return
	}
	for _, l := range lines[:keep] {
		fmt.Fprintf(sb, "%c%s\n", sign, l)
	}
	fmt.Fprintf(sb, "%c…（%d 行略。全 %d 行）\n", sign, len(lines)-2*keep, len(lines))
	for _, l := range lines[len(lines)-keep:] {
		fmt.Fprintf(sb, "%c%s\n", sign, l)
	}
}
