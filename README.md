# esa

esa (esa.io) の任意チームのドキュメントを **Chrome のログインセッション Cookie** で参照する読み取り専用 CLI。
Claude Code から esa の記事を検索・参照するために使う。トークン発行は不要。

- **macOS 専用**。Chrome の Cookie を macOS Keychain 経由で復号し、esa の Web UI が使う内部エンドポイントを叩く（トークン不要）
- **ログイン済みのプロファイルを自動検出**する（`-profile auto` が既定）。どの Chrome プロファイルで esa にログインしていても動く
- パスはすべて HOME 基準で解決し、**カレントディレクトリに一切依存しない**（ディレクトリを移動しても動作する）
- 読み取り専用（更新系は実装しない）

## インストール

Homebrew（tap 経由・推奨）:

```sh
brew install jiikko/tap/esa
```

Homebrew が第三者 tap の信頼を求める場合（Homebrew 6 以降）は、先に tap を信頼する:

```sh
brew trust jiikko/tap                 # tap 全体を信頼
# もしくは formula 単位で:  brew trust --formula jiikko/tap/esa
brew install jiikko/tap/esa
```

インストール時に Go（build 依存）が自動で入り、ソースからビルドされる。アップデートは `brew upgrade esa`、削除は `brew uninstall esa`。

go install（バイナリ名は `esa`）:

```sh
go install github.com/jiikko/esa-cli/cmd/esa@latest
```

バイナリは `$(go env GOBIN)`（未設定なら `$(go env GOPATH)/bin`）に置かれる。PATH に無ければ追加するか、`GOBIN` に PATH 上のディレクトリを指定する。

自分でビルド:

```sh
git clone https://github.com/jiikko/esa-cli
cd esa-cli
go build -o esa ./cmd/esa
```

生成された `esa` を PATH の通った場所（例: `~/bin`）に置くか、フルパスで呼ぶ。
pure Go（`modernc.org/sqlite`）なので `CGO_ENABLED=0` でビルド可。macOS 専用。

## 前提（認証）

- 対象の Chrome で `https://<team>.esa.io` に**ログイン済み**であること
- 初回実行時、macOS が Keychain アクセスの許可を求める → **「常に許可」** を選ぶ
- `~/Library/Application Support/Google/Chrome/` の読み取りに **フルディスクアクセス**が必要な場合がある
  （システム設定 → プライバシーとセキュリティ → フルディスクアクセス に、実行元のターミナル/アプリを追加）
- セッションが切れると内部エンドポイントは全パスで 404 を返す（非公開チームの挙動）。その場合は Chrome でログインし直す。

## 初回セットアップ（team を設定）

対象チームは特定サービスに依存しないため、最初に自分のチーム名を設定する（`https://<team>.esa.io` の `<team>` 部分）。

おすすめは対話式ウィザード（team・使用する Chrome プロファイルをまとめて設定。候補プロファイルの
ログイン中メールと「認証が通るか」を見ながら選べる）:

```sh
esa setup
```

個別に設定してもよい:

```sh
esa config set team myteam     # 一度設定すれば以後不要（~/.config/esa-cli/config.yml に保存）
# もしくは環境変数: export ESA_TEAM=myteam
# もしくは都度: esa search -team myteam '<クエリ>'
```

未設定のまま実行すると、設定方法を案内して終了する（終了コード 2）。

## コマンド

```
esa search <クエリ...>   記事を検索（結果は TSV。表示カラムは -c で変更可）
esa show   <番号|URL>    記事本文を Markdown（YAML front matter 付き）で出力
esa meta   <番号|URL>    記事のメタ情報を出力（-comments でコメントも）
esa revisions <番号|URL> リビジョン一覧（番号 / 更新日時 / 更新者）
esa config               設定ファイル(config.yml)の表示・編集
esa setup                対話式セットアップ（team と Chrome プロファイルを設定）
esa help                 ヘルプ
```

- ヘルプは 2 段構え: `esa --help` でサブコマンド一覧、`esa <サブコマンド> --help` で各コマンドの詳細。
- `<番号>` は記事 URL 末尾の数値。`esa show https://<team>.esa.io/posts/28025` のように URL でも可。
- 終了コード: `0`=成功 / `1`=実行時エラー（認証切れ・404・ネットワーク等）/ `2`=使い方の誤り。
  引数不足時は「使い方 + `--help` への案内」を stderr に出して `2` で終わる。

### 例

```sh
esa search 'in:設計 updated:>2026-01-01'
esa search 'title:ガイドライン wip:false'
esa show 28025
esa show 28025 | sed -n '1,120p'   # 長い記事は範囲を絞る
esa show 28025 | glow -            # Markdown を色付きで読む
esa meta 28025 -comments
esa search -json 'キーワード'            # JSON で受け取る（Claude Code / スクリプト向け）
```

検索クエリ `q` の構文は esa の Web 検索と同じ（`in:` `title:` `body:` `#tag` `@user` `updated:>YYYY-MM` `sort:` など）。
詳細は `esa search --help`。

## 検索の表示カラム

`search` の出力はタブ区切りで、先頭にヘッダ行が付く。表示するカラムは `-c` / `-columns` で変更できる。

```sh
esa search 'in:設計'                                   # 既定: number,created,updated,author,name
esa search -c number,updated,author,name 'キーワード'
esa search -c number,created,updated,created_by,updated_by,url 'title:ガイドライン'
esa search -no-header -c number,url 'キーワード' | awk -F'\t' '{print $2}'   # スクリプト向け
esa search -json 'in:設計' | jq -r '.[].number'                  # JSON で受け取る
```

指定可能なカラム:

| カラム | 内容 |
|---|---|
| `number` | 記事番号 |
| `name` | カテゴリ/タイトル（full_name） |
| `title` | タイトルのみ |
| `category` | カテゴリ |
| `url` | URL |
| `created` / `updated` | 作成日 / 更新日（`YYYY-MM-DD`） |
| `created_at` / `updated_at` | 作成日時 / 更新日時（ISO8601） |
| `author` / `updated_by` | 最終更新者の screen_name |
| `created_by` | 作成者の screen_name |
| `wip` | WIP かどうか |
| `tags` | タグ（カンマ連結） |

- 日付・author・category・tags 等は各記事の `/posts/N.json` を並行取得して埋める。多数ヒット時はやや時間がかかる。
- `-fast` を付けると詳細取得を省略して高速化する（`number` / `title` / `url` のみ確実で、日付・author 列は空になる）。
- `ESA_TOKEN` を設定している場合は公式 API が 1 リクエストで詳細まで返すため、`-fast` 相当の速さで全カラムが埋まる。
- ヘッダ行が不要なら `-no-header`（`awk -F'\t'` 等でパースしやすい）。

## オプション

| フラグ | 環境変数 | 既定 | 説明 |
|---|---|---|---|
| `-team <name>` | `ESA_TEAM` | （必須） | チーム名。`https://<team>.esa.io` の `<team>` |
| `-profile <name>` | `ESA_CHROME_PROFILE` | `auto` | Chrome のプロファイル。`auto` はログイン済みを自動検出 |
| `-json` | — | off | JSON で出力（search / meta / revisions。show は常に Markdown） |

search 専用: `-c` / `-columns`、`-no-header`、`-fast`、`-n <数>`、`-page <数>`。
meta 専用: `-comments`。

## プロファイルの自動検出

`-profile auto`（既定）は、esa にログイン済みの Chrome プロファイルを自動で探して使い、
結果を `~/.config/esa-cli/cache/` にキャッシュする（次回以降は高速）。
固定したい場合は下記の設定ファイル、`-profile "Profile 3"`、`export ESA_CHROME_PROFILE="Profile 3"` のいずれでも指定できる。

## 設定ファイル（config.yml）

よく使う値（使用プロファイル等）を保存できる。

- 場所: `$XDG_CONFIG_HOME/esa-cli/config.yml`（未設定なら `~/.config/esa-cli/config.yml`）
- 優先順位: **コマンドラインフラグ > 環境変数 > config.yml > 組み込み既定**
- キー: `profile` / `team`

> **`browser` キーは廃止しました（Chrome 専用）。** 以前あった `-browser` フラグ / `ESA_BROWSER` /
> config.yml の `browser` キーは削除済みです（issue 003）。理由は、Chrome 以外の対応表の値
> （Keychain のサービス名・Application Support のディレクトリ名）を実機で確認できないため。
> 既存の `browser:` 行は無視され、`esa config set`/`setup`/`config init` の書き戻しで消えます。
> （書き戻しは config.yml を `profile` / `team` から組み立て直すので、**手で書いたコメント行や
> 未知のキーも一緒に消えます**。これは以前からの挙動ですが、`browser` は「かつて正式なキーだった値」
> として初めてこれに当たります。残したい記述があれば書き戻し前に控えてください。）
> `esa config set browser X` は「不明なキー」エラーになります。
> プロファイルの検出キャッシュ名も `profile-<ブラウザ>-<team>` から `profile-<team>` に変わったため、
> 旧ファイルは孤児として残ります（実害は自動検出が一度だけ余計に走るだけ。手で消して構いません）。

```sh
esa config                          # 現在の有効な設定と出所(env/file/default)を表示
esa config set profile "Profile 3"  # 使用プロファイルを固定（自動検出をスキップ）
esa config init                     # ログイン済みプロファイルを自動検出して profile に保存
esa config path                     # config.yml のパスを表示
esa config get profile              # 保存値を表示
```

config.yml の例:

```yaml
team: myteam        # https://myteam.esa.io の myteam 部分
profile: Profile 3  # 使用する Chrome プロファイル（省略時は auto で自動検出）
```

> 補足: `search` を公式 API(api.esa.io) で高速化したい場合は環境変数 `ESA_TOKEN` を使う
> （config.yml のキーではない。未設定でも Chrome cookie で動くので通常は不要）。

## 補足

- 環境変数 `ESA_TOKEN` を設定すると、**search のみ**公式 API（`api.esa.io`）を使う（1 リクエストで全カラムを取得でき高速・安定）。
  未設定時は内部エンドポイントの検索 HTML から記事番号を取り、各記事 JSON で詳細を補完する。
  `show` / `meta` / `revisions` は常に Cookie を使う（`.md` 取得は内部エンドポイントの利点）。
- 取得内容は社内情報。外部サービスへの貼り付け・保存に注意。

## スクリプト・自動化から使う

典型的な流れは「検索して番号を得る → 本文を読む」で、AI エージェントやシェルスクリプトからも同じ:

```sh
esa search 'キーワード'   # まず絞り込む → 番号を得る（必要な列だけ -c で）
esa show <番号>           # 本文を Markdown で読む
```

- 出力をパースするなら `-json`、TSV で十分なら `-no-header`。
- 全体像は `esa --help`、各コマンドの詳細は `esa <サブコマンド> --help`。
