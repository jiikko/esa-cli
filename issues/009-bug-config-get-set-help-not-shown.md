# 009 bug: `esa config get/set --help` がヘルプを出さず、`--help` をキー名として扱う

起票日: 2026-09-29

## 症状（2026-09-29 に HEAD 08bda48 のビルドで実測）

| コマンド | rc | stdout | stderr |
|---|---|---|---|
| `esa config get --help` | 2 | 0 行 | `エラー: 不明なキー "--help"（指定可能: profile, team）` |
| `esa config get -h` | 2 | 0 行 | `エラー: 不明なキー "-h"（指定可能: profile, team）` |
| `esa config set --help` | 2 | 0 行 | `エラー: キーと値を指定してください。` |
| `esa config set -h` | 2 | 0 行 | `エラー: キーと値を指定してください。` |

issue 005 で決めた契約は「`--help` は明示的な要求なので stdout へ出して rc=0」（`cmd/esa/main.go` の `parseArgs` のコメント）。
`esa config --help` と `esa config init --help` はこの契約どおりだが、`get` / `set` だけが外れている。

## 原因

`cmd/esa/config_file.go` の `cmdConfig` は、`get` / `set` の引数を FlagSet を通さずに `args[1]` / `args[2]` として読む。
そのため `--help` は `configGet` / `configSet` にキー名として渡る（`set --help` は引数の数の検査で先に止まる）。

## 対応方針

- `get` / `set` の直後が `-h` / `--help` なら `configHelp` を stdout へ出して rc=0 にする（`config init` と同じ扱い）
- `TestSubcommandsGoThroughParseArgs` は main の switch から呼ばれる関数しか見ず、`cmdConfig` は除外に入っている。
  `config` の中のサブコマンドの `--help` はこの検査の外なので、`get` / `set` の `--help` の stdout と rc=0 をテストで直接固定する
- `esa config set --help profile` のように値が続く形で、`--help` をキーとして保存しないことも固定する（今も `configKeys` の検査で弾かれる）

## 経緯

issue 005 の敵対的レビューで見つかり、範囲外として 005 の「対応せず記録に留めたもの」の 3 に残していた。005 を閉じる際に起票した。
