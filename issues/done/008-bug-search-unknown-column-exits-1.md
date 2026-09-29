# 008 bug: `esa search -c` の不明なカラムが rc=1 になる（README の契約では 2）

起票日: 2026-09-29

## 症状（2026-09-29 に HEAD 08bda48 のビルドで実測）

| コマンド | rc | stderr |
|---|---|---|
| `esa search -c nosuch q` | **1** | `エラー: 不明なカラム "nosuch"。指定可能: number, name, …` |
| `esa search -c , q` | **1** | `エラー: 有効なカラムがありません` |

README の「コマンド」節は終了コードを `1`=実行時エラー（認証切れ・404・ネットワーク等）/ `2`=使い方の誤り と定めている。
カラムの指定の誤りは引数の誤りなので 2 のはず。どちらも認証より前（`cfg.requireTeam` の前）で止まるので、
esa への通信は起きていない。

## 原因

`cmd/esa/columns.go` の `parseColumns` が `fmt.Errorf` を返し、`cmd/esa/main.go` の `cmdSearch` がそのまま返している。
`exitCodeFor` は `*usageError` のときだけ 2 を返すので 1 になる。

## 対応方針

- `parseColumns` の 2 つのエラーを `usageError` にする（`エラー: ` の接頭辞と `詳細: esa search --help` の案内は、
  cmdSearch の他の使い方エラーの書式に揃える）
- rc=2 と stderr の文言をテストで固定し、`fmt.Errorf` へ戻す変異で red を見る

## 経緯

issue 005 の敵対的レビューで見つかり、範囲外として 005 の「対応せず記録に留めたもの」の 4 に残していた。005 を閉じる際に起票した。

## 進捗・結果（2026-09-29）

- [x] `parseColumns` の 2 つのエラーを `usageError` に（`エラー: ` の接頭辞と `詳細: esa search --help` の案内つき）
- [x] テスト `TestParseColumnsErrorsAreUsageErrors`（rc=2・書式・`cmdSearch` まで通しても team の検査より前に止まる）
- 実物で確認: `esa search -c nosuch q` → rc=2 / `esa search -c , q` → rc=2
- 変異で red: 不明なカラムを `fmt.Errorf` に戻す / 空の指定を `fmt.Errorf` に戻す（2 本）
- 敵対的レビュー（read-only のサブエージェント）: この修正は壊せなかった
