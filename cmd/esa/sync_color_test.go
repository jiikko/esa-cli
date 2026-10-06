package main

import (
	"os"
	"strings"
	"testing"
)

func TestWantColor(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		env      map[string]string
		want     bool
	}{
		{"端末", true, nil, true},
		{"パイプ・ファイル", false, nil, false},
		{"NO_COLOR", true, map[string]string{"NO_COLOR": "1"}, false},
		{"NO_COLOR が空なら付ける", true, map[string]string{"NO_COLOR": ""}, true},
		{"TERM=dumb", true, map[string]string{"TERM": "dumb"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wantColor(c.terminal, func(k string) string { return c.env[k] }); got != c.want {
				t.Errorf("wantColor = %v（%v のはず）", got, c.want)
			}
		})
	}
}

// テストの出力はパイプ（pty ではない）なので、色は付かないこと。
// ModeCharDevice で判定すると /dev/null が端末に見えるため、/dev/null でも付かないことを見る。
func TestSyncPaletteForNonTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if syncPaletteFor(null).on {
		t.Error("/dev/null を端末とみなした")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if syncPaletteFor(w).on {
		t.Error("パイプを端末とみなした")
	}
}

func colorTestPlan() syncPlan {
	return syncPlan{
		items: []syncPlanItem{
			// 削除する行の本文が "-- x"（差分では "--- x"）でも見出しと取り違えないこと。本文の ESC は無害化されること。
			{f: syncFile{rel: "foo.md", number: 1, body: "a\nb \x1b[2J\nkeep\n"}, status: syncChanged, old: "a\n-- x\nkeep\n"},
			{f: syncFile{rel: "new.md", number: 2, body: "hello\n", warns: []string{"気をつけて"}}, status: syncNew},
		},
		skipped:   []syncSkipped{{number: 3, category: "c", name: "skip"}},
		leftovers: []string{"x.md.esa-sync-tmp"},
	}
}

func TestPrintSyncPlanColors(t *testing.T) {
	var sb strings.Builder
	printSyncPlan(&sb, colorTestPlan(), false, syncPalette{on: true})
	out := sb.String()

	for _, want := range []string{
		"  " + sgrBold + sgrYellow + "~ foo.md" + sgrReset + "  変更 ",
		"  " + sgrBold + sgrGreen + "+ new.md" + sgrReset + "  新規 ",
		"    " + sgrBold + "--- ローカル foo.md" + sgrReset + "\n",
		"    " + sgrBold + "+++ esa #1" + sgrReset + "\n",
		"    " + sgrCyan + "@@ -1,3 +1,3 @@" + sgrReset + "\n",
		"    " + sgrRed + "--- x" + sgrReset + "\n",
		"    " + sgrGreen + `+b \x{1b}[2J` + sgrReset + "\n",
		"    " + " a\n",
		"    " + sgrGreen + "+hello" + sgrReset + "\n",
		"  " + sgrYellow + "注意:" + sgrReset + " new.md（esa #2）: 気をつけて\n",
		"  " + sgrDim + "- c/skip  書き出さない esa #3",
		"  " + sgrYellow + "注意:" + sgrReset + " 前回中断したとき",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
	// 色のエスケープを取り除いたら、ESC は 1 つも残らないこと（本文の ESC が素通りしていない）
	stripped := out
	for _, sgr := range []string{sgrReset, sgrBold, sgrDim, sgrRed, sgrGreen, sgrYellow, sgrCyan} {
		stripped = strings.ReplaceAll(stripped, sgr, "")
	}
	if strings.Contains(stripped, "\x1b") {
		t.Errorf("色以外の ESC が出力に残っている:\n%q", stripped)
	}

	// 色なしの出力は、色ありから色を取り除いたものと同じ（色の有無で中身が変わらない）
	var plain strings.Builder
	printSyncPlan(&plain, colorTestPlan(), false, syncPalette{})
	if plain.String() != stripped {
		t.Errorf("色なしの出力が、色ありから色を除いたものと違う:\n色なし: %q\n除いた: %q", plain.String(), stripped)
	}
}

// 大きいファイルの差分（largeDiffSummary）の説明行は淡色、見出しは太字、行は赤・緑。
func TestPrintSyncPlanColorsLargeDiff(t *testing.T) {
	n := 2100 // 2100*2100 > maxDiffCells
	a := strings.Repeat("same\n", n)
	b := strings.Repeat("same\n", n-1) + "changed\n"
	var sb strings.Builder
	printSyncPlan(&sb, syncPlan{items: []syncPlanItem{{f: syncFile{rel: "big.md", number: 9, body: b}, status: syncChanged, old: a}}},
		false, syncPalette{on: true})
	out := sb.String()
	for _, want := range []string{
		"    " + sgrBold + "--- ローカル big.md" + sgrReset + "\n",
		"    " + sgrDim + "（大きいため",
		"    " + sgrRed + "-same" + sgrReset + "\n",
		"    " + sgrGreen + "+changed" + sgrReset + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}
