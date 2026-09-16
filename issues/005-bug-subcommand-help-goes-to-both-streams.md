# 005 bug: サブコマンドの --help がストリームを取り違えている

カテゴリ: bug / 優先度: 中

## 症状（実測。v0.1.4 相当の HEAD 42c0dcc をビルドして計測）

`--help` は明示的な要求なので **stdout** へ出し、フラグの誤りの usage は **stderr** へ出す、
というのが `cmd/esa/main.go` の `parseArgs` が宣言している契約。**6 サブコマンド中 5 つで
成立していない**。

| コマンド | rc | stdout | stderr | 判定 |
|---|---|---|---|---|
| `esa --help`（トップ） | 0 | 39 | 0 | ✅ 正しい（flag を通らない） |
| `esa config --help` | 0 | 17 | 0 | ✅ 正しい（`cmdConfig` の switch が処理） |
| `esa search --help` | 0 | **57** | **57** | ❌ 同一内容が両方に出る |
| `esa show --help` | 0 | **15** | **15** | ❌ 同上 |
| `esa meta --help` | 0 | **18** | **18** | ❌ 同上 |
| `esa revisions --help` | 0 | **13** | **13** | ❌ 同上 |
| `esa setup --help` | 0 | **0** | **11** | ❌ stdout に出ない |
| `esa config init --help` | 0 | **0** | **17** | ❌ stdout に出ない |

実害は「`esa search --help | less` のようにパイプへ流すと、stderr 側の 57 行が端末へ
そのまま溢れる」「`2>/dev/null` を付けると help が消える経路と消えない経路が混在する」。

## 原因（2 つの異なる形）

esa には FlagSet の構築経路が **2 つ**あり、バグの形も 2 種類になっている。

**経路 A（`search` / `show` / `meta` / `revisions`）** — 各コマンドが同じ 3 行を自前で書いている
（`main.go:338-340, 404-406, 434-436, 491-493`。**4 箇所の重複**）:

```go
fs := flag.NewFlagSet("search", flag.ContinueOnError)
fs.SetOutput(os.Stderr)
fs.Usage = func() { fmt.Fprint(os.Stderr, searchHelp) }
```

`flag` は `ErrHelp` を返す**前に**自分で `fs.Usage` を呼ぶ。そのため `fs.Usage` が stderr へ
出したうえに、`parseArgs` が `ErrHelp` を見て stdout へも出す → **二重**。

**経路 B（`setup` / `config init`）** — `newFlagSet`（`main.go:62`、`flag.ExitOnError`）を使い、
`parseArgs` を通さず素の `fs.Parse(args)` を呼ぶ（`setup.go:46` / `config_file.go:254`）。
`ExitOnError` なので `--help` では `fs.Usage`（stderr）を出して `os.Exit(0)` し、
`parseArgs` の stdout 経路に**到達しない**。

なお `newFlagSet` は経路 B からしか使われておらず、その `help` 引数は `fs.Usage` 専用。

## 方針

newrelic-nrql-cli が同型のバグを `0f799ca` で修正済み（あちらは経路が 1 つだけだったので
こちらの方が少し手間）。同じ設計に寄せる:

- `newFlagSet` を `ContinueOnError` + `SetOutput(os.Stderr)` + **`fs.Usage` は no-op** にする
- usage を出す責務を `parseArgs` に一本化する。`--help` は渡された stdout へ、
  フラグの誤りのときだけ `fs.Output()`（stderr）へ
- `parseArgs` に stdout の書き出し先を引数で渡し、ストリームの契約をテストできるようにする
- **6 経路すべてを `newFlagSet` + `parseArgs` に揃える**（経路 A の重複 4 箇所が消える）

## 受け入れ条件

- [x] `search` / `show` / `meta` / `revisions` / `setup` / `config init` の `--help` が
      stdout のみに出る（stderr 0 行 / rc=0）
- [x] `esa --help` と `esa config --help` は従来どおり（回帰させない）
- [x] フラグの誤りは rc=2 で stderr のみ。経路 A の stderr は**修正前とバイト単位で同一**
- [x] FlagSet 構築の重複 4 箇所を `newFlagSet` に寄せる
- [x] ストリームの契約をテストで固定し、変異で red を見る
- [x] **各コマンドがその経路を通っていること**も固定する（下記 🚨）

## 🚨 承知の上の挙動変更

`esa setup -bogus` / `esa config init -bogus` の **stderr が 1 行増える**（12 → 13 行）。

修正前は `ExitOnError` が `os.Exit(2)` していたため `エラー: flag provided but not defined: -bogus`
の行が出なかった。修正後は `parseArgs` が `usageError` を返し、`main` の通常のエラー表示を
通るのでこの行が付く。rc は 2 のまま変わらず、経路 A（`search` 等）は元からこの行が出ていたので、
**6 コマンドの挙動が揃う方向**の変更。経路 A のバイト列は変えない。

## 進捗

実装は 1 commit。`newFlagSet` を `ContinueOnError` + `fs.Usage` no-op にし、usage を出す責務を
`parseArgs` に一本化。6 経路すべてを `newFlagSet` + `parseArgs` に揃えた（経路 A の重複 4 箇所が消えた）。

### 結果（修正前後を stdout / stderr / rc を分離して全 13 ケース実測し、byte 比較）

| コマンド | rc | stdout | stderr | 修正前との差 |
|---|---|---|---|---|
| `search --help` | 0 | 57 | **0**（← 57） | stderr の重複が消えた。stdout は**バイト単位で不変** |
| `show --help` | 0 | 15 | **0**（← 15） | 同上 |
| `meta --help` | 0 | 18 | **0**（← 18） | 同上 |
| `revisions --help` | 0 | 13 | **0**（← 13） | 同上 |
| `setup --help` | 0 | **11**（← 0） | **0**（← 11） | 出力先が stderr → stdout へ移動。**内容は同一** |
| `config init --help` | 0 | **17**（← 0） | **0**（← 17） | 同上 |
| `config --help` | 0 | 17 | 0 | **完全一致**（回帰なし） |
| `search -bogus x` | 2 | 0 | 59 | **完全一致**（バイト単位） |
| `show -bogus x` | 2 | 0 | 17 | **完全一致** |
| `meta -bogus x` | 2 | 0 | 20 | **完全一致** |
| `revisions -bogus x` | 2 | 0 | 15 | **完全一致** |
| `setup -bogus` | 2 | 0 | 13（← 12） | `エラー: ...` が 1 行増える（下の 🚨 のとおり） |
| `config init -bogus` | 2 | 0 | 19（← 18） | 同上 |

「消えた stderr」「移動した stdout」はいずれも**修正前の内容とバイト単位で同一**であることを
`diff` で確認済み（help が失われたのではなく、重複の除去と出力先の移動だけ）。

`go build` / `go vet` / `gofmt -l` / `go test ./...` はいずれもクリーン。

### 変異検証（6 本。面ごとに 1 本ずつ、ケース名単位で判定）

各変異は当てる前に diff で意図した箇所だけが変わったことと、`go build` が rc=0 で
**ビルドできている**ことを確認した。

| # | 変異（退行の実形） | red になったテスト |
|---|---|---|
| M1 | `fs.Usage` を旧形（`os.Stderr` へ直接 help）に戻す | `TestParseArgsHelpGoesToStdoutOnly` のみ |
| M2 | フラグ誤り時の usage 出力を消す | `TestParseArgsFlagErrorGoesToStderrOnly` のみ |
| M3 | `--help` を stdout でなく stderr へ出す | `TestParseArgsHelpGoesToStdoutOnly` のみ |
| M4 | 正常系でも help を出してしまう | 上記 3 本すべて |
| M5 | 自前 FlagSet + `Usage` を張る新コマンドを足す | `TestFlagSetConstructionIsCentralized` のみ |
| M6 | `setup` を素の `fs.Parse` に戻す | `TestSubcommandsGoThroughParseArgs` のみ |

M4 は `TestParseArgsQuietOnSuccess` が空振りでないことの確認用（他の変異では緑のままだったため、
「何も守っていないテスト」でないかを別途当てた）。

🚨 M4 は**最初 perl の構文エラーで変異が当たらず**、diff が空のまま「緑」が出た。
diff を読む手順を踏んでいなければ「このテストは何も検出しない」と誤診していた。

### 🚨 テストが os.Stderr を pipe で捕捉している理由

退行の実体は `fs.Usage = func() { fmt.Fprint(os.Stderr, help) }` という
**プロセスの `os.Stderr` へ直接書く**形なので、`FlagSet` の output を buffer に
差し替えるだけのテストでは M1 を素通りする。`os.Stderr` 自体を pipe に差し替えて捕まえる。
`newFlagSet` は構築時の `os.Stderr` を `SetOutput` で焼き込むので、**捕捉の内側で呼ぶ**こと。

### 🚨 振る舞いのテストだけでは足りなかった

`TestParseArgs*` の 3 本は `parseArgs` の振る舞いしか固定しておらず、
**各コマンドがその経路を通っているか**は 1 mm も守らない。M5（自前 FlagSet を組む新コマンドを足す）は
3 本すべてを素通りした。issue 003 のレビューで受けた「回帰ゲートが 1 面しか守っていない」の
同型なので、AST でセントラル化と配線を固定するテストを 2 本足した
（`TestFlagSetConstructionIsCentralized` / `TestSubcommandsGoThroughParseArgs`）。
どちらも走査が空振りしたら落ちる canary を持たせてある。

## レビュー（敵対的 read-only サブエージェント / codex は使えないため）

**修正本体は 3 つの攻撃観点すべてで「壊せなかった」**と報告された。

- 引数解析に失敗したのに config.yml へ書き込む経路 → **無い**（6 箇所すべてガード済み）
- `--help` の後に後続処理へ進む経路 → **無い**（6 箇所すべて同一形）
- `fs.Usage` の no-op 化で usage が出なくなったケース → **既存 6 コマンドでは無い**
  （`-n abc` の型不一致 / 引数不足 / 不正カラム名の stderr が修正前とバイト一致）

壊れたのは全部「修正を守るゲート」の側だった。以下は自分で再現してから対応した。

### 採用して直したもの（すべて変異で red を確認）

| 指摘 | 実測した壊れ方 | 対応 |
|---|---|---|
| **P1-3** `parseArgs` の戻り値を捨てても全 green | `setup.go` で戻り値を捨てると build / vet / test すべて rc=0 のまま、`esa setup --help </dev/null` が**対話ウィザードを完走して config.yml を上書き**した | `TestParseArgsResultIsConsumed`（`ExprStmt` として呼んでいたら落とす） |
| **P1-1** `newFlagSet` + 素の `fs.Parse` の新コマンド | help が **1 バイトも出ず** `エラー: flag: help requested` が漏れ、rc が 0→1。**修正前より悪い無音の壊れ方** | `TestFlagSetParseOnlyInsideParseArgs` |
| **P1-2(a)** 複合リテラル `&flag.FlagSet{Usage: ...}` + `Init` | 経路 A の二重出力がそのまま復活 | 構築ゲートに `CompositeLit` の `Usage` フィールドと `Init` を追加 |
| **P1-2(b)** 別名 import `gflag "flag"` | 経路 B（stdout に出ない）がそのまま復活 | パッケージ名の決め打ち（`X.Name == "flag"`）をやめ、`Sel.Name` だけで判定 |
| **P1-2(d)** 対象一覧が 6 名ハードコード | 7 つ目のコマンドは追跡対象外 | `main` の switch から AST で導出。除外は理由を書いた `exempt` map のみ |
| **P2-1** コマンド側の `SetOutput` 上書き | `esa search -bogus x` が stdout 58 行 = パイプが壊れる | `SetOutput` も `newFlagSet` の中だけに制限 |
| **P3-2** `captureStdio` が panic 時に goroutine を漏らす | — | close を `defer` へ移動 |

**🚨 ゲートの脅威モデルを `help_streams_test.go` のヘッダに明記した。** 構文 gate は
typecheck を通る迂回が原理的に無限にあるので「全部塞ぐ」を目標にしない。止める対象を
「うっかり書く典型形」に限り、**止めないと決めた形**（別パッケージへの移動 / reflect /
`newFlagSet` を間接に包む helper）を列挙して、そこは review の責務だと書いた。

### 迂回の変異検証（5 形。いずれもビルド可能を確認してから実行）

| 迂回 | red になったテスト |
|---|---|
| A: 複合リテラルの `Usage` + `Init` | `TestFlagSetConstructionIsCentralized` |
| B: 別名 import + `ExitOnError` | `TestFlagSetConstructionIsCentralized` |
| C: `newFlagSet` + 素の `fs.Parse` | `TestFlagSetParseOnlyInsideParseArgs` |
| D: `parseArgs` の戻り値を捨てる | `TestParseArgsResultIsConsumed` |
| E: コマンド側で `SetOutput(os.Stdout)` | `TestFlagSetConstructionIsCentralized` |

🚨 A は 1 回目、シェルのエスケープが Go ソースへ漏れて**ビルド不能**になった。
「ビルド不能」は red でも green でもない第 3 の結果として扱い、当て直した。

### 対応せず記録に留めたもの（却下理由）

1. **`t.Parallel()` を足すと `captureStdio` のグローバル差し替えが壊れる**（P3-2(b)）。
   repo に `t.Parallel` は現在 **0 件**で、足した瞬間に壊れる latent な trigger。
   ゲートを新設するより、`captureStdio` のコメントに書いて留める（機構を足すこと自体が
   新しい失敗の入口になるため）。**再評価の trigger**: このパッケージに `t.Parallel` を
   足したくなったとき。
2. **config.yml が壊れていると `--help` でも stderr に警告 1 行が出る**（P3-1）。
   `registerCommon` → `loadFileConfig` が `parseArgs` より前に走るため。実害は
   「警告 1 行」で `2>/dev/null` で消える。受け入れ条件の「stderr 0 行」は
   **正常な config.yml のとき**の主張である、と下に注記した。
3. **`esa config get --help` / `config set --help` は rc=2**（`--help` がキー名として
   扱われる）。`config` 配下で `--help` が効くのは `config` 自身と `config init` だけ。
   変更前から同じでスコープ外。
4. **`esa search -c nosuch q` が rc=1**（README の「2=使い方の誤り」と食い違う）。
   `parseColumns` の error を `usageError` に包めば直るが、本 issue の範囲外。別 issue 候補。
5. **`esa search foo --help` は `--help` がクエリの一部になる**。`checkNoTrailingFlags` は
   `f.formal` にある名前しか弾かず `help`/`h` は formal に無い。変更前から同じ。

## 🚨 受け入れ条件の注記

「`--help` の stderr 0 行」は **config.yml が正常なとき**の主張。壊れた config.yml が
あると `registerCommon` 経由の警告が 1 行出る（`esa --help` と `esa config --help` は
`registerCommon` を通らないので 0 行のまま）。新テストは `newFlagSet` + `parseArgs` を
直接叩くので、この形はどの変異でも赤くならない。

## 残タスク

- 未着手: なし
- スコープ外（別 issue 候補。根拠は上の「却下理由」）:
  - `esa search -c nosuch` の rc が 1（README の契約では 2）
  - `esa config get/set --help` が効かない
- 未検証: 認証が要る経路（`esa search` / `esa show` の実通信）。ただし今回の変更は
  引数解析より手前で完結しており、認証経路には触れていない。
