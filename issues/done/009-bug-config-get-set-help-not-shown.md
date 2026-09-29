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

## 進捗・結果（2026-09-29）

- [x] `cmdConfig` で、`get` / `set` の引数にヘルプの要求があれば `configHelp` を stdout へ出して rc=0（`containsHelpArg`）
  - flag パッケージがヘルプとみなす 4 つの綴り（`-h` / `-help` / `--h` / `--help`）はどの位置でも。`help` はキーの位置だけ
    （値の位置では team 名として正当: `help.esa.io`。`esa config set team help` は従来どおり保存する）
  - 壊れた config.yml の検査より前で判定する（ヘルプを見るのに設定ファイルは要らない）
- [x] 起票時の方針より広げた: **`esa config set profile --help` が profile に `--help` を保存していた**（修正前のビルドで実測、rc=0）ので、値の位置の `--help` もヘルプにした
- [x] テスト `TestConfigGetSetHelp`（10 の形）/ `TestConfigSetHelpWithBrokenConfig` / `TestConfigSetTeamNamedHelp`
- 変異で red: 判定を外す / help をどの位置でもヘルプにする / 壊れた config.yml の検査の後に置く / `-help` と `--h` を外す（4 本）
- 敵対的レビューの指摘で直したもの: `-help` と `--h` を見落としていた（`esa search -help` はヘルプになるのに `config get -help` は「不明なキー」だった）
- 受容: `-h` / `--help` は値としても使えない（team はサブドメインなので - で始められない。profile は Chrome のディレクトリ名）
