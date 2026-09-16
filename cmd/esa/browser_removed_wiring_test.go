package main

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// 🚨 issue 003（Chrome 専用化）で削除したブラウザ選択の機構が、production へ戻ってこないことを固定する。
//
// これが無いと退行を誰も止められない。実測（2026-09-16）: `registerCommon` に
// `fs.String("browser", ...)` を 1 行戻す変異を当てたところ、`go build` は成功し
// `go test ./...` は **17 本すべて緑のまま**だった。config キーの回帰テスト
// （config_browser_removed_test.go）は `configKeys` の面しか見ておらず、
// 「フラグ」「環境変数」「対応表」の 3 面はどれも無防備だった。
//
// 削除した面は 4 つあるので、面ごとに落ちるテストを持つ（1 本にまとめると、
// どれか 1 面が落ちた時点で red になり、残りの面が守られていないことが見えない）。

// 面①: -browser フラグが登録されないこと。
func TestBrowserFlagIsNotRegistered(t *testing.T) {
	// registerCommon は loadFileConfig() 経由で設定ファイルを読む。読むだけだが、
	// 実行環境の ~/.config/esa-cli/config.yml に結果を左右されないよう隔離する。
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var cfg config
	registerCommon(fs, &cfg)

	// canary: registerCommon が実際にフラグを登録していることを先に固定する。
	// これが無いと、registerCommon が何もしなくなった日に「browser は無い」が
	// 空振りで緑になり、このテストは何も守らなくなる。
	for _, name := range []string{"team", "profile", "json"} {
		if fs.Lookup(name) == nil {
			t.Fatalf("canary: -%s が登録されていない（registerCommon が壊れている。"+
				"この状態では -browser の不在を確かめても意味が無い）", name)
		}
	}

	if f := fs.Lookup("browser"); f != nil {
		t.Errorf("-browser フラグが復活している（issue 003 で削除した）: usage=%q", f.Usage)
	}
}

// 面②③④: ESA_BROWSER の参照・browserProfiles / browserProfile の復活を production ソースで止める。
//
// grep ではなく AST で見る（wiring_test.go と同じ理由）。grep だと、この検査自身の
// ソースに含まれる "ESA_BROWSER" の文字列に一致して、本物が復活しても緑のままになりうる。
// テストファイルは走査対象から外してあるので、この説明文自身は拾われない。
func TestBrowserSelectionIsAbsentFromProductionSource(t *testing.T) {
	idents, strs := productionIdentsAndStrings(t)

	// canary: 走査が実際にソースを読めていることを、必ず在るものが見つかることで確かめる。
	// 抽出が 0 件なら「違反 0 件」も自動的に成立してしまう（緑の空振り）。
	if !idents["chromeSupportSubdir"] {
		t.Fatalf("canary: chromeSupportSubdir が見つからない（走査が空振りしている。"+
			"識別子 %d 個 / 文字列 %d 個しか拾えていない）", len(idents), len(strs))
	}
	if !strs["ESA_CHROME_PROFILE"] {
		t.Fatal("canary: 文字列 \"ESA_CHROME_PROFILE\" が見つからない（文字列リテラルの走査が空振りしている）")
	}

	for _, name := range []string{"browserProfiles", "browserProfile", "listBrowserProfiles"} {
		if idents[name] {
			t.Errorf("識別子 %s が production に復活している（issue 003 で削除した"+
				"ブラウザ対応表。未確認の Keychain サービス名・ディレクトリ名を並べると"+
				"「別ブラウザの領域を読みに行く」事故になる）", name)
		}
	}
	if strs["ESA_BROWSER"] {
		t.Error(`環境変数 "ESA_BROWSER" の参照が production に復活している（issue 003 で削除した）`)
	}
}

// productionIdentsAndStrings は production の .go（テストを除く）に現れる
// 識別子名と文字列リテラルの集合を返す。コメントは含まない（コメントで廃止を
// 説明している箇所を違反として拾わないため）。
func productionIdentsAndStrings(t *testing.T) (idents, strs map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0) // 0 = コメントを読み込まない
	if err != nil {
		t.Fatalf("ソースの解析に失敗: %v", err)
	}
	idents = map[string]bool{}
	strs = map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.Ident:
					idents[v.Name] = true
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						strs[strings.Trim(v.Value, "`\"")] = true
					}
				}
				return true
			})
		}
	}
	return idents, strs
}
