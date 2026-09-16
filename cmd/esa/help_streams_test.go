package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// --help とフラグの誤りで、出力先のストリームが分かれていることを固定する（issue 005）。
//
// 🚨 FlagSet の output を buffer に差し替えるだけのテストでは退行を検出できない。
// 退行の実体は `fs.Usage = func() { fmt.Fprint(os.Stderr, help) }` という
// **プロセスの os.Stderr へ直接書く**形なので、FlagSet の output をどう差し替えても
// そこには現れない。だから os.Stderr 自体を pipe に差し替えて捕まえる。
//
// 🚨 newFlagSet は構築時の os.Stderr の値を fs.SetOutput で焼き込む。
// そのため newFlagSet の呼び出しは**必ず捕捉の内側**で行うこと（captureStdio の中）。

// captureStdio は fn の実行中の os.Stdout / os.Stderr を捕まえて返す。
func captureStdio(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	// 読み側は goroutine で吸い出す。パイプのバッファ（64KB）で書き手が
	// ブロックすると、テストが deadlock して「遅い」ではなく「止まる」になる。
	// （1MB を両ストリームへ流しても完走することを実測で確認済み。）
	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, outR); outCh <- b.String() }()
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, errR); errCh <- b.String() }()

	// 🚨 復元と書き手側の close は defer に置く。fn が panic したとき、
	// close されないと io.Copy の goroutine 2 本が永久に残る。
	// os.File.Close の二重呼び出しは ErrClosed を返すだけなので、正常系で
	// 先に閉じてから defer がもう一度呼んでも害は無い。
	restore := func() {
		os.Stdout, os.Stderr = origOut, origErr
		_ = outW.Close()
		_ = errW.Close()
	}
	defer restore()

	fn()
	restore() // 正常系: 読み出す前に書き手を閉じる（閉じないと io.Copy が返らない）
	return <-outCh, <-errCh
}

// --help は stdout だけに出ること（stderr には 1 バイトも出さない）。
func TestParseArgsHelpGoesToStdoutOnly(t *testing.T) {
	const help = "ESA-HELP-MARKER\nつかいかた: esa dummy\n"

	stdout, stderr := captureStdio(t, func() {
		// 🚨 newFlagSet は捕捉の内側で呼ぶ（SetOutput が os.Stderr を焼き込むため）。
		fs := newFlagSet("dummy")
		done, err := parseArgs(fs, help, []string{"--help"}, os.Stdout)
		if err != nil {
			t.Errorf("--help でエラーを返した: %v", err)
		}
		if !done {
			t.Error("--help なのに helpRequested が false")
		}
	})

	if stdout != help {
		t.Errorf("stdout が help と一致しない:\n got: %q\nwant: %q", stdout, help)
	}
	if stderr != "" {
		t.Errorf("--help なのに stderr へ %d バイト出ている（二重出力の退行）: %q",
			len(stderr), stderr)
	}
}

// フラグの誤りのときは stderr だけに出て、stdout は汚さないこと。
// stdout が汚れると `esa search --help | jq` のようなパイプが壊れる。
func TestParseArgsFlagErrorGoesToStderrOnly(t *testing.T) {
	const help = "ESA-HELP-MARKER\nつかいかた: esa dummy\n"

	stdout, stderr := captureStdio(t, func() {
		fs := newFlagSet("dummy")
		done, err := parseArgs(fs, help, []string{"-bogus"}, os.Stdout)
		if err == nil {
			t.Error("未定義フラグなのにエラーを返さなかった")
		}
		if done {
			t.Error("フラグの誤りなのに helpRequested が true")
		}
		var ue *usageError
		if err != nil && !asUsageError(err, &ue) {
			t.Errorf("usageError を期待したが %T", err)
		}
	})

	if stdout != "" {
		t.Errorf("フラグの誤りなのに stdout へ %d バイト出ている: %q", len(stdout), stdout)
	}
	// flag 自身のエラー行 + usage の両方が stderr に出る。
	if !strings.Contains(stderr, "ESA-HELP-MARKER") {
		t.Errorf("stderr に usage が出ていない: %q", stderr)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("stderr に flag のエラー文が出ていない: %q", stderr)
	}
}

// 正常な引数のときは、どちらのストリームにも何も出さないこと。
// （ここが汚れていると、上の 2 本が「常に何か出ている」状態を見逃す。）
func TestParseArgsQuietOnSuccess(t *testing.T) {
	const help = "ESA-HELP-MARKER\n"

	stdout, stderr := captureStdio(t, func() {
		fs := newFlagSet("dummy")
		done, err := parseArgs(fs, help, []string{"arg1"}, os.Stdout)
		if err != nil || done {
			t.Errorf("正常な引数で done=%v err=%v", done, err)
		}
	})

	if stdout != "" || stderr != "" {
		t.Errorf("正常時に出力がある: stdout=%q stderr=%q", stdout, stderr)
	}
}

func asUsageError(err error, target **usageError) bool {
	ue, ok := err.(*usageError)
	if ok {
		*target = ue
	}
	return ok
}

// --- ここから下は「配線のゲート」。上の 3 本とは守るものが違う ---
//
// 上の 3 本は parseArgs の**振る舞い**しか固定していない。各サブコマンドが
// 実際にその経路を通っているかは別の主張で、振る舞いのテストは 1 mm も守らない
// （実測: 自前で FlagSet を組む新コマンドを足す変異は 3 本すべてを素通りした）。
//
// 🚨 脅威モデル（この gate が何を止め、何を止めないか）
//
// 止めるもの: **うっかり書く典型形**。新しいサブコマンドを足す人が、既存コマンドを
// コピペし損ねて自前の FlagSet を組む / 素の fs.Parse を呼ぶ / parseArgs の戻り値を
// 捨てる、という形。issue 005 のバグは実際にこの形で 4 箇所に増殖していた。
//
// 止めないもの（意図的。ここは review の責務）:
//   - このディレクトリの外（別パッケージ）へコードを移した場合。走査は
//     parser.ParseDir(".") の 1 ディレクトリだけを見る
//   - reflect や生成コード経由で FlagSet を組む形
//   - newFlagSet を間接的に包む helper を新設し、その中で契約を破る形
//   - ローカル変数を経由せずに関数の戻り値を直接使う込み入った式
//
// 構文 gate は typecheck を通る迂回が原理的に無限にあるので、「全部塞ぐ」を目標に
// しない（塞ぐたびに新しい迂回が出て収束しない）。規則の軸は**構文でなく効果**に
// 置いてある — 「どう書いたか」ではなく「FlagSet の構築・出力先・Parse・戻り値の
// 消費が 1 箇所に集まっているか」を見る。

// 規則 1〜3: FlagSet の構築・出力先・Usage は newFlagSet の中だけ。
//
// 各コマンドが自前で Usage を張ると --help が stdout と stderr の両方に出る（issue 005 経路 A）。
// 自前で ExitOnError の FlagSet を作ると --help が stdout へ出ない（同 経路 B）。
func TestFlagSetConstructionIsCentralized(t *testing.T) {
	type site struct{ fn, what string }
	var sites []site

	forEachProductionFunc(t, func(fnName string, body ast.Node, _ map[string]bool) {
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// 🚨 パッケージ名を "flag" と決め打ちしない。`import gflag "flag"` の
				// 別名で迂回できてしまう（実測で素通りを確認した）。名前だけで見る。
				switch sel.Sel.Name {
				case "NewFlagSet":
					sites = append(sites, site{fnName, "flag.NewFlagSet"})
				case "Init": // fs.Init(name, errorHandling) は NewFlagSet と等価
					sites = append(sites, site{fnName, "FlagSet.Init"})
				case "SetOutput":
					sites = append(sites, site{fnName, "SetOutput"})
				}
			case *ast.AssignStmt:
				for _, lhs := range v.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Usage" {
						sites = append(sites, site{fnName, "Usage への代入"})
					}
				}
			case *ast.CompositeLit:
				// &flag.FlagSet{Usage: ...} の形（AssignStmt では見えない）
				for _, elt := range v.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Usage" {
							sites = append(sites, site{fnName, "複合リテラルの Usage フィールド"})
						}
					}
				}
			}
			return true
		})
	})

	// canary: 走査が空振りしていないこと（0 件なら違反 0 件も自動的に成立する）。
	if len(sites) == 0 {
		t.Fatal("canary: FlagSet の構築も出力先の設定も 1 件も見つからない（AST 走査が空振りしている）")
	}

	for _, s := range sites {
		if s.fn != "newFlagSet" {
			t.Errorf("%s が %s() の中にある。FlagSet の構築・出力先・Usage は newFlagSet に寄せること"+
				"（自前で組むと --help のストリームが壊れる。issue 005）", s.what, s.fn)
		}
	}
}

// 規則 4: FlagSet の Parse を呼ぶのは parseArgs の中だけ。
//
// newFlagSet の Usage は no-op なので、素の fs.Parse を呼ぶコマンドでは
// --help が **1 バイトも出ず**、Go 内部の "flag: help requested" が
// エラーとして漏れて rc も 1 になる（実測）。修正前より悪い無音の壊れ方になる。
func TestFlagSetParseOnlyInsideParseArgs(t *testing.T) {
	var callers []string
	found := 0

	forEachProductionFunc(t, func(fnName string, body ast.Node, imports map[string]bool) {
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Parse" {
				return true
			}
			// html.Parse / url.Parse のようなパッケージ関数は対象外。
			// レシーバが import 名なら変数ではないので除く。
			if x, ok := sel.X.(*ast.Ident); ok && imports[x.Name] {
				return true
			}
			found++
			if fnName != "parseArgs" {
				callers = append(callers, fnName)
			}
			return true
		})
	})

	if found == 0 {
		t.Fatal("canary: 変数に対する .Parse( が 1 件も見つからない（走査が空振りしている）")
	}
	for _, fn := range callers {
		t.Errorf("%s() が FlagSet の Parse を直接呼んでいる。parseArgs を通すこと"+
			"（素の Parse だと newFlagSet の no-op Usage のせいで --help が無音になる。issue 005）", fn)
	}
}

// 規則 5: parseArgs の戻り値を捨てないこと。
//
// 🚨 これは ExitOnError をやめたことで**新しく必要になった**ゲート。
// 旧実装では Parse の内側で os.Exit していたため「戻り値を無視する呼び出し側」は
// 構造的に無害だった。今は戻り値を捨てると --help の後も処理が続く。
// 実測: setup.go で戻り値を捨てると go build / go vet / go test すべて rc=0 のまま、
// `esa setup --help </dev/null` が対話ウィザードを完走して config.yml を上書きした。
func TestParseArgsResultIsConsumed(t *testing.T) {
	var discarded []string
	total := 0

	forEachProductionFunc(t, func(fnName string, body ast.Node, _ map[string]bool) {
		ast.Inspect(body, func(n ast.Node) bool {
			// ExprStmt = 式を文として書いた形 = 戻り値をどこにも渡していない
			es, ok := n.(*ast.ExprStmt)
			if !ok {
				return true
			}
			if call, ok := es.X.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "parseArgs" {
					discarded = append(discarded, fnName)
				}
			}
			return true
		})
		// 呼び出しの総数も数える（canary 用）
		ast.Inspect(body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "parseArgs" {
					total++
				}
			}
			return true
		})
	})

	if total == 0 {
		t.Fatal("canary: parseArgs の呼び出しが 1 件も見つからない（走査が空振りしている）")
	}
	for _, fn := range discarded {
		t.Errorf("%s() が parseArgs の戻り値を捨てている。`if done, err := parseArgs(...); "+
			"err != nil || done { return err }` の形にすること"+
			"（捨てると --help を出した後も処理が続く。issue 005）", fn)
	}
}

// 規則 6: main の switch から呼ばれる各コマンドが parseArgs を通っていること。
//
// 🚨 対象一覧をハードコードしない。main の switch から AST で導出するので、
// 7 つ目のサブコマンドを足した人も自動で追跡対象になる
// （ハードコードしていた版では、新コマンド 3 本がどれも素通りした）。
func TestSubcommandsGoThroughParseArgs(t *testing.T) {
	// parseArgs を通さなくてよいコマンドと、その理由。
	// ここへ足すときは理由を書くこと（黙って除外しない）。
	exempt := map[string]string{
		"cmdConfig": "自前のサブコマンド switch で -h/--help を stdout へ処理する。" +
			"フラグを取る config init は configInit 側で parseArgs を通す",
	}

	commands := commandsFromMainSwitch(t)
	if len(commands) == 0 {
		t.Fatal("canary: main の switch からコマンドを 1 つも抽出できない（走査が空振りしている）")
	}
	// canary: 既知のコマンドが抽出できていること（switch の形が変わって
	// 空に近い集合になっても気づけるように、実在するものを 1 つ名指しで確認する）。
	if !commands["cmdSearch"] {
		t.Fatalf("canary: cmdSearch が main の switch から抽出できていない（抽出結果: %v）", commands)
	}

	callsParseArgs := map[string]bool{}
	forEachProductionFunc(t, func(fnName string, body ast.Node, _ map[string]bool) {
		ast.Inspect(body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "parseArgs" {
					callsParseArgs[fnName] = true
				}
			}
			return true
		})
	})

	for cmd := range commands {
		if callsParseArgs[cmd] {
			continue
		}
		if why, ok := exempt[cmd]; ok {
			t.Logf("%s は parseArgs を通さない（除外理由: %s）", cmd, why)
			continue
		}
		t.Errorf("%s() が parseArgs を呼んでいない（素の fs.Parse だと --help が "+
			"stdout へ出ず、フラグの誤りも exitCodeFor を通らない。issue 005）", cmd)
	}
}

// commandsFromMainSwitch は main() の switch の各 case から呼ばれている
// 関数名（cmdSearch 等）を集める。
func commandsFromMainSwitch(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	forEachProductionFunc(t, func(fnName string, body ast.Node, _ map[string]bool) {
		if fnName != "main" {
			return
		}
		ast.Inspect(body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			ast.Inspect(cc, func(m ast.Node) bool {
				if call, ok := m.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, "cmd") {
						out[id.Name] = true
					}
				}
				return true
			})
			return true
		})
	})
	return out
}

// forEachProductionFunc は production の .go（テストを除く）の各関数に fn を適用する。
// imports はそのファイルの import 名の集合（パッケージ関数呼び出しを見分けるのに使う）。
func forEachProductionFunc(t *testing.T, fn func(name string, body ast.Node, imports map[string]bool)) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("ソースの解析に失敗: %v", err)
	}
	seen := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			imports := map[string]bool{}
			for _, imp := range file.Imports {
				name := strings.Trim(imp.Path.Value, `"`)
				if i := strings.LastIndex(name, "/"); i >= 0 {
					name = name[i+1:]
				}
				if imp.Name != nil {
					name = imp.Name.Name
				}
				imports[name] = true
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				seen++
				fn(fd.Name.Name, fd.Body, imports)
			}
		}
	}
	if seen == 0 {
		t.Fatal("canary: production の関数が 1 つも見つからない（走査が空振りしている）")
	}
}
