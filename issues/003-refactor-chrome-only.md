# 003 refactor: Chrome 専用に絞る（-browser / ESA_BROWSER / config.yml browser を廃止）

カテゴリ: refactor / 優先度: 中

## 背景

esa-cli は Brave / Chromium / Edge / Vivaldi を対応表（`cmd/esa/cookies.go` の `browserProfiles`）に
持っているが、**対応表の値（Keychain のサービス名・Application Support 配下のディレクトリ名）は
実機で確認しないと正しいか分からない**。手元で確認できるのは Chrome だけで、未確認の値を並べると
「動くように見えて別ブラウザの領域を読みに行く」形の事故になる。

同じ判断を先に下しているのが newrelic-nrql-cli で、理由はあちらの issue 005
（`feat-widen-environment-support`）にある。Chrome 専用実装の先例は
newrelic-nrql-cli の `cmd/nrql/cookies.go`（復号仕様は両 repo で共通）。

本 issue は esa-cli を同じ方針に揃える。

## 決定（ユーザー承認済み）

**完全に削除**する。互換の警告期間は置かない。

- `-browser` フラグ / `ESA_BROWSER` / config.yml の `browser` キーを消す
- `browserProfiles` マップと `browserProfile` 型を消し、Chrome の値を定数にする

### 承知の上の副作用

1. `cmd/esa/config_file.go` の `loadFileConfig` は素の `yaml.Unmarshal` なので、
   **未知キーは黙って無視される**。`Browser` フィールドを消すと既存の `browser: Brave` は
   無言で無視され、Chrome が使われる。
2. さらにその一段先: `loadFileConfig` が落としたキーは構造体に入らないため、
   **`saveFileConfig` の書き戻し（`esa config set` / `esa setup` / `esa config init` のいずれか 1 回）で
   既存の `browser:` 行がファイルから消える**。ユーザーが消せと指示していない行を消す書き込みになるが、
   キー自体を廃止する変更なので挙動は変えない（ここに記録するに留める）。
3. `esa config set browser X` は「不明なキー」エラー（rc=2）になる。
4. `profileCachePath` からブラウザ成分を落とす（`profile-<browser>-<team>` → `profile-<team>`）。
   既存の `~/.config/esa-cli/cache/profile-Chrome-<team>` は**孤児になる**。実害は
   「自動検出が一度だけ余計に走り、新しい名前でキャッシュが書き直される」だけ。
   **孤児を消す掃除機構は作らない**（破壊的操作を新設する側に回るため）。

## 受け入れ条件

- [x] `browserProfile` 型 / `browserProfiles` マップを削除し、Chrome の値を定数にする
- [x] Chrome 専用にした**理由**を `cookies.go` の先頭に 🚨 コメントで残す（次の audit が対応表を再生成しないため）
- [x] `-browser` フラグ / `ESA_BROWSER` / `fileConfig.Browser` / `configKeys["browser"]` を削除する
- [x] `setup.go` のブラウザ選択ステップを削除し、ステップ番号を振り直す
- [x] ブラウザ成分を持つ識別子を Chrome 命名に揃える（`listBrowserProfiles` → `listChromeProfiles` 等）
- [x] エラー文・ヘルプ・雛形コメントから `-browser` / `ESA_BROWSER` の案内を消す
- [x] README から `-browser` の行・`browser` キーの記述を消し、廃止を 1 行残す
- [x] `esa config set browser X` が使い方エラーになることをテストで固定し、変異で red を見る
- [x] `go build ./...` / `go test ./...` が通る

## 進捗

実装は `refactor: Chrome 専用に絞る（-browser / ESA_BROWSER / config.yml browser を廃止）` の 1 commit。
着手順は「`browserProfile` 型と `browserProfiles` マップを先に消す → `go build` の compile error で
Go 側の全呼び出し箇所を機械的に列挙 → 潰す」。grep が担当したのはコンパイラに見えない層
（エラー文・ヘルプ・雛形コメント・README）だけ。

### 結果（実測）

**全数勘定**: 着手前 `grep -rn -i 'browser|brave|chromium|vivaldi|edge'`（`tmp/` 除く）で
**78 件 / 6 ファイル**（内訳: config_file.go 18 / cookies.go 16 / main.go 7 / profile.go 20 /
README.md 3 / setup.go 14。`git show HEAD:<file>` で復元して再集計し一致を確認）。

🚨 **作業後の生存件数は「本 issue を除いた数」で書く。** この issue 自身が `browser` を含む行を
持つため、issue を数に入れると**書き足すたびに自分の集計が古くなる**（最初に「32 件」と書いたが、
その後の追記で 46 件になっていた。しかも内訳表の合計は 31 で本文の 32 とも食い違っていた）。
commit 時点で本 issue を除いた生存は **34 件**（`grep ... | grep -v 'issues/003'` で機械的に集計）。
内訳は全て「削除したこと自体の記録」または「復活を止める検査」:

| 生存箇所 | 件数 | 正当化 |
|---|---|---|
| `cmd/esa/browser_removed_wiring_test.go` | 14 | 復活を止める AST 検査（後述 P2-1 の対応で追加） |
| `cmd/esa/config_browser_removed_test.go` | 9 | 廃止キーの復活を止めるテストの本文・テスト名 |
| `cmd/esa/config_file.go`（型定義と `configKeys` の直上） | 5 | 廃止の 🚨 コメント（未知キーの扱い・復活時の壊れ方） |
| `README.md` | 5 | 廃止の告知ブロック |
| `cmd/esa/cookies.go` | 1 | Chrome 専用にした理由の 🚨 コメント |

production コードからの消滅は識別子単位でも確認（`ESA_BROWSER` / `cfg.browser` / `fc.Browser` /
`browserProfiles` / `browserProfile` / `listBrowserProfiles` / `-browser` がいずれも **0 件**）。

🚨 **数え方の盲点**: 上の grep はローマ字 `browser` しか見ておらず、**カタカナ「ブラウザ」を
原理的に拾えない**。別途 `grep -rn 'ブラウザ'` で数え直したところ 9 件あり、production の残存は
0 件（全て廃止理由の 🚨 コメント）で実害は無かったが、**同じ数え方は同じ盲点を共有する**ので、
次に同種の監査をするときは日本語の語も軸に入れること。

**ビルド・テスト**: `go build ./...` rc=0 / `go vet ./...` rc=0 / `go test ./...` rc=0（`ok`）/
`gofmt -l` 出力なし。stdout・stderr は分けて取得して両方確認した。

**変異検証**: 削除した面ごとに 1 本ずつ、計 4 本。全段同時に当てるとどれか 1 つが落ちて
red になり、**残りの面が守られていないことが見えない**ので、必ず面ごとに当てる。
各変異は当てる前に diff で意図した箇所だけが変わったことを確認し、`go build` / `go vet` が
rc=0 で**ビルドできている**ことを（テスト実行とは別に）確認した
（ビルド不能の緑を「検知できなかった」と誤読しないため）。判定はスイートの rc ではなく
**ケース名ごとの PASS/FAIL 一覧**で行った。

| # | 変異（退行の実形） | red になったテスト | 他 |
|---|---|---|---|
| A | `configKeys` に `"browser": true` を戻す | `TestConfigSetRejectsRemovedBrowserKey` / `TestConfigGetRejectsRemovedBrowserKey` | 他 17 本 PASS |
| B | `registerCommon` に `fs.String("browser", ...)` を戻す | `TestBrowserFlagIsNotRegistered` のみ | 他 20 本 PASS |
| C | `envOr("ESA_BROWSER", ...)` の参照を戻す | `TestBrowserSelectionIsAbsentFromProductionSource` のみ | 他 20 本 PASS |
| D | `browserProfile` 型 + `browserProfiles` マップを戻す | `TestBrowserSelectionIsAbsentFromProductionSource` のみ | 他 20 本 PASS |

変異はすべて revert し、バックアップとの diff が空・`MUTANT` / `"browser": true` の残骸 0 件を確認済み。

🚨 **B は当初「緑のまま」だった**（下の P2-1）。`browser_removed_wiring_test.go` を足して初めて
red になる。「識別子単位で grep して 0 件」は**一度きりの観測であって退行を止める機構ではない**。

**実バイナリでの挙動確認**（Chrome のログインは不要な経路のみ）:

- `esa search -browser Brave foo` → rc=2 / stderr `flag provided but not defined: -browser`
- `esa config set browser Brave` → rc=2 / stderr `エラー: 不明なキー "browser"（指定可能: profile, team）`、書き込みなし
- `esa --help` / `esa search --help` / `esa config --help` に `browser` の語が 0 件

**テストのサンドボックス**: 変異下では `configSet("browser", ...)` が早期 return せず
`saveFileConfig` まで到達し、**利用者の本物の `~/.config/esa-cli/config.yml` を書き換える**。
「落ちるはずだから書かれない」は落ちなくなった時にだけ効かないので、テストに
`t.Setenv("XDG_CONFIG_HOME", t.TempDir())` を入れて**実行前に**書き込み先を閉じ込めた。
変異実行の前後で本物の config.yml の md5（`5c66b697030c8c369c91a7e0a002b306`）と mtime が
不変であることを確認し、隔離が効いていることを実測した。

## レビュー（観点を分けた read-only サブエージェント 2 体）

このマシンでは codex が使えないため、CLAUDE.md「レビュー方針」に従い観点を分けた
read-only サブエージェントで代替した。①敵対的（壊す / 外した防御がマスクしていたもの）
②取りこぼし（半端に残っていないか・文書整合）。

### 採用して直したもの

**P2-1（実害。最重要）**: 回帰ゲートが、削除した 4 面のうち `configKeys` の 1 面しか
守っていなかった。`registerCommon` に `fs.String("browser", ...)` を 1 行戻す変異を当てると
**ビルドは成功しテストは 17 本すべて緑**だった（指摘を鵜呑みにせず自分で再現）。
`cmd/esa/browser_removed_wiring_test.go` を新設し、①`-browser` フラグの不在
（`flag.FlagSet` に登録されないこと。`-team`/`-profile`/`-json` が登録されることを canary に置き、
`registerCommon` が空振りしたら落ちるようにした）②`ESA_BROWSER` の文字列参照
③`browserProfiles` / `browserProfile` / `listBrowserProfiles` の識別子——を AST 走査で固定した
（grep だと検査自身のソースに一致して素通りする）。走査が空振りしたら落ちるよう
`chromeSupportSubdir` と `"ESA_CHROME_PROFILE"` を canary に置いた。

**P2-2**: 新規テストの `t.Setenv` サンドボックスは、契約が守られている限り一度も実行されない
（`configSet` が早期 return するため）。「緑だから隔離も効いている」とは言えないので、
効くことを変異で確かめた事実と、消すときの再確認手順をテストのコメントに明記した。

**P3-2**: 書き戻しの破壊範囲が本 issue の当初記述より広い。`saveFileConfig` は config.yml を
`profile` / `team` から組み立て直すので、**手書きのコメント行や未知キーも一緒に消える**
（機構自体は変更前からある）。README の廃止告知にこの点を追記した。

**数値の誤り**: 上の「全数勘定」節を参照。生存件数の集計が自己言及で陳腐化していた。

### 対応せず記録に留めたもの（却下理由）

次の監査が同じ指摘を再生成しないよう、根拠つきで残す。

1. **廃止キー `browser` を検出して stderr に 1 行出す**（敵対レビューの推奨 2）。
   指摘の中身は妥当で、特に「同じマシンの Chrome に**別アカウント**で esa にログインしていると、
   auto 検出が黙ってそれを拾い、config.yml には Brave と書いてあるのに別 identity の
   セッションで社内ドキュメントを読む」形は本物の懸念（ただし敵対レビュー側も実機では未再現）。
   **それでも実装しない**: ユーザーは「互換の警告期間は置かない」「既存の `browser: Brave` は
   無言で無視され Chrome になる。これを了解している」と明示的に決定しており、警告の追加は
   その決定を覆すため。**再評価の trigger**: 「設定と違うアカウントで読んでいた」という報告が
   出たとき、または廃止から十分時間が経って警告のコストが下がったとき。
2. **`-profile` 経由のパス脱出**（`cookies.go` の `filepath.Join(..., profile)` に `../..` を
   含むプロファイル名を渡すと Chrome の領域外を読める）。**変更前から同一の式**であり
   （`git show HEAD:cmd/esa/cookies.go` で確認）、削除した `browserProfiles[browser]` の検査が
   守っていたのは browser 成分だけで profile 成分は元から無防備。到達には Keychain の解錠が要る
   （= 本人のマシンで本人が実行）ためローカル自傷系。**本変更が作った穴ではない**。
   関連: `listChromeProfiles` は Local State の `info_cache` キーを無検査で使うが、
   `fallbackProfiles` は `Default` / `Profile *` に絞る（同じ値の生成元で片方だけフィルタがある）。
3. **`cmdSetup` / `configInit` に `fileConfigProblem()` のガードが無い**（`config set` にはある）。
   壊れた config.yml をゼロ値から書き直す失敗モードがこの 2 経路に残っている。これも変更前から同じ。
4. **キャッシュ名の衝突**: 旧 `profile-Chrome-foo` と新 `profile-<team>` は、team 名が
   `Chrome-foo` のとき同一パスになる（APFS の大文字小文字非区別も含む）。実害は
   「1 回だけ誤ったプロファイル名をキャッシュから読み、`authOK()` が false で走査へ落ちる」まで。

### 壊せなかったと明記されたもの

- 削除した `browserProfiles[browser]` の存在チェックが副次的に守っていたもの（typo 検出 /
  空文字の早期失敗 / 各経路の前提固定）は、削除後すべて**構造的に moot**。typo は `flag` が
  未定義フラグとして rc=2 で弾き、`cfg.browser` はフィールドごと存在せず、分岐は
  コンパイル時定数になった。値の集合が 5 → 1、実行時決定 → コンパイル時定数なので
  **厳密に安全側**であり、新しい failure mode は見つからなかった。
- テストのサンドボックス回避経路（`sync.Once` のキャッシュ / `t.Parallel()` /
  `configDir()` 以外のパス決定）はいずれも成立しなかった。

## 残タスク

- 未着手: なし
- スコープ外（別 issue の候補。根拠は上の「却下理由」）:
  - 他ブラウザへの再対応（やるなら実機で `security find-generic-password` と
    Application Support 配下のディレクトリ名を確認してから）
  - `-profile` 経由のパス脱出（変更前から存在）
  - `cmdSetup` / `configInit` の `fileConfigProblem()` ガード欠落（変更前から存在）
  - サブコマンドの `--help` が stdout と stderr の両方へ全文を出す（変更前から存在。
    newrelic-nrql-cli 側で修正済みの同型バグ）
- 未検証:
  - 実際の Chrome ログインセッションを使った E2E（`esa search` / `esa show` の実行）。
    Chrome のログイン状態に依存するためユーザーに委ねる。
  - `esa setup` / `esa config init` の書き戻しで `browser:` 行が消えること。
    両者は `config set` と同じ `saveFileConfig(fileConfig)` を通るのでコード経路としては同一だが、
    実行に認証が要るため**実測できていない**（`esa config set` 経由では実測で確認済み）。
