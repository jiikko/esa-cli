# esa

esa (esa.io) の任意チームのドキュメントを **Chrome のログインセッション Cookie** で参照する CLI（esa へは書き込まない）。
Claude Code から esa の記事を検索・参照するために使う。トークン発行は不要。

- **macOS 専用**。Chrome の Cookie を macOS Keychain 経由で復号し、esa の Web UI が使う内部エンドポイントを叩く（トークン不要）
- **ログイン済みのプロファイルを自動検出**する（`-profile auto` が既定）。どの Chrome プロファイルで esa にログインしていても動く
- パスはすべて HOME 基準で解決し、**カレントディレクトリに一切依存しない**（ディレクトリを移動しても動作する）
- esa へは書き込まない（更新系は実装しない）。`esa sync` はカテゴリの記事をローカルのディレクトリへ書き出すだけ

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

formula の正本は [jiikko/homebrew-tap](https://github.com/jiikko/homebrew-tap) の `Formula/esa.rb` **のみ**。
このリポジトリには写しを置かない（2 箇所に同じものがあると、片方だけ直したときに静かにずれるため）。

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
esa meta   <番号|URL>    記事のメタ情報を出力（-comments でコメントも / -copy でリンクをクリップボードへ）
esa revisions <番号|URL> リビジョン一覧（番号 / 更新日時 / 更新者）
esa config               設定ファイル(config.yml)の表示・編集（esa sync の対象も同じファイルの sync: に書く）
esa setup                対話式セットアップ（team と Chrome プロファイルを設定）
esa sync [名前...]       カテゴリ配下の記事をローカルのディレクトリへ書き出す（既定は dry-run）
esa help                 ヘルプ
```

- ヘルプは 2 段構え: `esa --help` は概要とサブコマンドの一覧だけ。オプション・共通オプション（`-team` / `-profile`）・終了コード・認証の
  詳細は `esa <サブコマンド> --help`（`esa sync add --help` / `esa sync list --help` も）に出る。
  **`--help` は stdout に出る**ので `esa search --help | less` のようにパイプへ流せる。
  フラグの誤りの usage は stderr（rc=2）。
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
esa meta -copy https://<team>.esa.io/posts/28025   # タイトルをリンクにした形でクリップボードへ
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
  取得に失敗した記事は基本情報（`number` / `title` / `url`）のみで残し、失敗件数と最初のエラーを stderr に 1 行警告する（終了コードは 0）。
- `-fast` を付けると詳細取得を省略して高速化する（`number` / `title` / `url` のみ確実で、日付・author 列は空になる）。
- `ESA_TOKEN` を設定している場合は公式 API が 1 リクエストで詳細まで返すため、`-fast` 相当の速さで全カラムが埋まる。
- ヘッダ行が不要なら `-no-header`（`awk -F'\t'` 等でパースしやすい）。

## オプション

| フラグ | 環境変数 | 既定 | 説明 |
|---|---|---|---|
| `-team <name>` | `ESA_TEAM` | （必須） | チーム名。`https://<team>.esa.io` の `<team>`（英小文字・数字・ハイフン。大文字は小文字に正規化） |
| `-profile <name>` | `ESA_CHROME_PROFILE` | `auto` | Chrome のプロファイル。`auto` はログイン済みを自動検出 |
| `-json` | — | off | JSON で出力（search / meta / revisions。show は常に Markdown） |

search 専用: `-c` / `-columns`、`-no-header`、`-fast`、`-n <数>`、`-page <数>`。
meta 専用: `-comments`、`-copy`。

フラグは `show` / `meta` / `revisions` では番号の前後どちらに書いてもよい（`search` はクエリより前に置く）。

## プロファイルの自動検出

`-profile auto`（既定）は、esa にログイン済みの Chrome プロファイルを自動で探して使い、
結果を `~/.config/esa-cli/cache/` にキャッシュする（次回以降は高速）。
Cookie DB が無い・esa の Cookie が無いプロファイルは黙って飛ばす。Cookie DB が壊れている・読めない（アクセス拒否を含む）・
復号に全件失敗したプロファイルも飛ばすが、認証済みのプロファイルが見つからなかったときはその理由をエラーに添える
（アクセス拒否があれば、フルディスクアクセスとプロファイルの所有者・パーミッションの両方を確認するよう案内する）。
認証の確認がそのプロファイルだけの理由で失敗したとき（401/403/404 以外の 4xx・リダイレクトループ等）も同じく飛ばして理由を添える。
認証を確認できないとき（ネットワーク断・タイムアウト・429・5xx）や Keychain の問題はどのプロファイルでも同じ結果になるので、
未ログインとは扱わずにその場でエラーにする（残りのプロファイルは試さない）。
自動検出がネットワーク・5xx 等で止まったとき、特定のプロファイルの Cookie が原因と思われるなら `-profile` で別のプロファイルを
指定するか、エラーに表示されるキャッシュファイルを削除して再実行する。
`-profile` を明示指定した場合も使う前に認証を 1 回確かめるが、確認できなくても（理由を問わず）止めずに理由付きの警告を
stderr に出して続行する（自動判定が誤ったときの逃げ道。エラーにするのは Cookie を読めないときだけ）。
固定したい場合は下記の設定ファイル、`-profile "Profile 3"`、`export ESA_CHROME_PROFILE="Profile 3"` のいずれでも指定できる。

## 設定ファイル（config.yml）

よく使う値（使用プロファイル等）を保存できる。

- 場所: `$XDG_CONFIG_HOME/esa-cli/config.yml`（未設定なら `~/.config/esa-cli/config.yml`）
- 優先順位: **コマンドラインフラグ > 環境変数 > config.yml > 組み込み既定**
- キー: `profile` / `team` / `sync`（`sync` は `esa sync` の対象。下の「カテゴリをローカルのディレクトリへ書き出す」）

> **`browser` キーは廃止しました（Chrome 専用）。** 以前あった `-browser` フラグ / `ESA_BROWSER` /
> config.yml の `browser` キーは削除済みです（issue 003）。理由は、Chrome 以外の対応表の値
> （Keychain のサービス名・Application Support のディレクトリ名）を実機で確認できないため。
> 既存の `browser:` 行は読まれません（ファイルには残ります）。
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
sync:               # esa sync の対象（esa sync add で追加できる）
  - name: skills
    category: Users/me/skills
    dir: ~/.claude/skills
```

- `esa config set` / `esa config init` / `esa setup` と `esa sync add` は、自分のキー（`profile` / `team`、または `sync`）だけを
  書き換えて書き戻す。**手で書いたコメント・他のキー・並びは残る**（issue 011。以前は `profile` / `team` から組み立て直して、
  コメントや未知のキーを消していた）。書き戻すと YAML の整形（字下げ・引用符）はそろう。
- 読めない config.yml（壊れた YAML・`---` で区切った複数の文書・最上位が `key: value` の並びでない）には書き込まない。
- config.yml がシンボリックリンク（dotfiles の実体を指す等）なら、リンクを残したまま実体へ書く。一時ファイルに書いてから置き換えるので、
  ハードリンクは切れる。既存のファイルの権限は保ち、読み取り専用のファイルには書かない。

> 補足: `search` を公式 API(api.esa.io) で高速化したい場合は環境変数 `ESA_TOKEN` を使う
> （config.yml のキーではない。未設定でも Chrome cookie で動くので通常は不要）。

## カテゴリをローカルのディレクトリへ書き出す（`sync`）

esa のカテゴリ配下の記事を、ローカルのディレクトリへ Markdown のファイルとして書き出す（esa → ローカルの一方向）。
Claude Code の skill や rule を esa で管理して手元へ配る、といった用途を想定している。esa への書き込みは無い。

```sh
esa sync add             # 対象を対話式で登録（カテゴリを開いたときの esa の URL をそのまま貼ってもよい）
esa sync                 # 全対象の差分を表示（書き込まない）
esa sync skills --apply  # 1 対象を書き込む
esa sync list            # 登録済みの対象
```

- 対象は `config.yml`（上の「設定ファイル」）の `sync:` に書く。手で編集してもよく、`esa sync add` の追記もコメントを残す。
  **v0.1.8 までの `sync.yml` は読まない**。残っていれば、`esa sync` がエラーで移し方を案内する
  （`sync.yml` の `targets:` の下の項目を、`config.yml` の `sync:` の下へそのまま移し、`sync.yml` を消す）。

  ```yaml
  sync:
    - name: skills               # esa sync <name> で指定する名前
      category: Users/me/skills  # esa のカテゴリ
      dir: ~/.claude/skills      # 書き出し先（絶対パスか ~ 始まり）
  ```

- 対応の規則: `Users/me/skills/foo/SKILL` → `~/.claude/skills/foo/SKILL.md`。サブカテゴリがディレクトリ、
  記事名がファイル名になり、`.md` を付ける（`.md` で終わる記事名はそのまま）。中身は本文の Markdown で、
  esa の記事情報を front matter として足すことはしない（本文に書いた front matter はそのまま）。改行は LF にそろえる。**WIP の記事も対象**。
- 既定は dry-run（新規・変更の一覧と、変更の差分を出すだけ）。`--apply` で書き込む。
- 書くのは esa にある記事のファイルだけで、**esa で消した記事のローカルのファイルは消さない**。
  ローカルで編集したファイルは上書きする（dry-run の差分に出る）。
- 1 件でも問題（記事の取得失敗・同じファイルになる 2 記事・書き出し先がシンボリックリンク等）があれば、
  その対象は 1 件も書かない。書き込みは dir の外へ出られない（`os.Root` 経由）。
- 大文字小文字・Unicode の正規化（NFC / NFD）だけが違う 2 記事は、macOS の既定のファイルシステムでは同じファイルになるので
  エラーにする。dry-run の表示では制御文字・見えない文字を `\x{1b}` の形にエスケープする（ファイルの中身は変えない）。
- `--apply` が書くのはその時点の esa の内容（dry-run の後に esa 側が変われば、変わった内容を書く。書く前に差分は出る）。
- 同じ dir への `--apply` は同時に 1 本だけ（ロックは設定ディレクトリの `locks/` に置く）。中断で残った一時ファイル
  （`*.esa-sync-tmp`）は dry-run で知らせ、次の `--apply` で消す（今回書き出す記事の分だけ。esa で改名・削除した記事の分は手で消す）。
- Claude Code の skill にするなら、記事名を `SKILL` にする（`<カテゴリ>/foo/SKILL` → `<dir>/foo/SKILL.md`）。ファイル名は記事名の大文字小文字のまま。
  記事名を人が読める題にしたいときは、次の「書き出し先の指定」を使う。
- **書き出し先の指定**: 記事名に関係なく、記事のカテゴリのディレクトリからの相対パスへ書く
  （カテゴリ `Users/me/skills` の記事で `foo/SKILL.md` なら `~/.claude/skills/foo/SKILL.md`、
  `Users/me/skills/team` の記事なら `~/.claude/skills/team/foo/SKILL.md`）。書き方は 2 通り（両方書くとエラー）:
  1. **skill の front matter の `metadata` の下**（issue 013。skill ならこちら）:

     ```yaml
     ---
     name: foo
     description: ...
     metadata:
       esa-sync: foo/SKILL.md
     ---
     ```

     front matter は本文の一部なので、**そのまま書き出す**（esa の記事とローカルのファイルが同じになる）。
     `metadata` は Claude Code の skill の仕様で「自前のツールが読む任意の map」とされ、Claude Code は中身を解釈しない。
     front matter とみなすのは、本文の 1 行目がちょうど `---` で、その後に `---` だけの行があるときだけ（Claude Code と同じ）。
     書き損じは黙って記事名の規則に戻さずエラーにする: `metadata` の外（最上位など）の `esa-sync` / `esa_sync` / `esaSync`、
     文字列でない・空の値、指定の重複、閉じの `---` の無い・1 行目が `---` だけでない（BOM・後ろの空白）・YAML として読めない・
     中に `---` の行がある front matter の中の指定らしい行（esa〜sync の直後にコロンがあり `.md` を含む行。閉じの無いときは最初の空行まで）。
     YAML として読めるときは、区切りを除くとちょうど `esasync` になるキーで、値が `.md` で終わるもの
     （水平線の `---` で始まる普通の記事や、`esa sync: …` のような説明の文は止めない）。
     字面の検出はすべての崩し方を拾わないので、**結果でも知らせる**: front matter に `description` がある（= skill らしい）記事なのに
     指定が無く `<ディレクトリ>/SKILL.md` 以外へ書くときは、dry-run と `--apply` の出力に「注意:」を出す（変更なしの記事にも毎回出す）。
     skill でないファイル（`.claude/agents/*.md` など）で注意を消すには、`metadata.esa-sync` に今の書き出し先のパスを書く。
  2. **本文の最後の行のコメント**（issue 010。front matter の無いファイル向け）: `<!-- esa-sync: foo/SKILL.md -->`
     - その行は書き出すファイルから取り除く。
     - 形は厳密に 1 通り（空白は半角 1 つずつ、行頭に空白を置かない）。全角のコロン・大文字・ゼロ幅の文字・em dash などで崩れた指定
       （`esa`〜`sync` の後にコロンと `.md` のパスが続く行）、本文の先頭に書いた指定、指定が 2 行続くもの、`--` を含むもの
       （HTML のコメントの中に書けない）はエラーにする。
       崩れの検出は書き損じに気づかせる補助で、すべての崩し方は拾わない。指定が効かなかった記事は dry-run に記事名のパスで出るので、そこで確かめる。
  - パスは `.md` で終わる相対パス。`..` で上へ出る・絶対パス・前後の空白・見えない文字は拒否する。衝突の検査は記事名の規則と同じ。
  - dry-run では「書き出し先は本文の指定による」と表示する。書き出し先の変更は dry-run で確かめる。
- esa で改名・削除した記事のファイルはローカルに残る（dry-run にも出ない）。`~/.claude/rules` のように置くだけで読まれる場所では、手で消すこと。
- 記事の一覧は検索（`in:"<カテゴリ>" sort:number-asc`）のページ送りで集め、2 回続けて同じ一覧になるまで取り直す。
  検索の索引への反映が遅れると、作ったばかりの記事がまだ出ないことがある。

## 補足

- 環境変数 `ESA_TOKEN` を設定すると、**search のみ**公式 API（`api.esa.io`）を使う（1 リクエストで全カラムを取得でき高速・安定）。
  未設定時は内部エンドポイントの検索 HTML から記事番号を取り、各記事 JSON で詳細を補完する。
  `show` / `meta` / `revisions` は常に Cookie を使う（`.md` 取得は内部エンドポイントの利点）。
- 取得内容はチーム内の非公開情報。外部サービスへの貼り付け・保存に注意。
- Cookie DB は作業領域（`~/Library/Caches/esa-cli/extract`、0700）へコピーしてから読み、そのコピーは
  ①正常終了・エラー ②シグナル（Ctrl-C 等）③次回起動時の掃除（`kill -9` 等で残ったもの）の 3 経路で削除する。
  作業領域がシンボリックリンク・他人の所有・group / other に権限がある状態なら使わずに止める（v0.1.5 から。以前は `$TMPDIR/esa-cookie`）。
- `-wal` / `-shm` を読めなくても、本体の Cookie DB に esa の Cookie があればそれを使う。無いときは読めなかったことを理由として添えて次のプロファイルへ進む（v0.1.5 から）。
- Chrome の Cookie の復号・一時コピーの後始末・プロファイルの列挙は、slack-cli / newrelic-nrql-cli と共有する
  [`github.com/jiikko/dotfiles/src/chromecookie`](https://github.com/jiikko/dotfiles/tree/master/src/chromecookie) が持つ。
  **直すときはあちらを直し**、`go get github.com/jiikko/dotfiles/src/chromecookie@master` で取り込み直す（tag は打たない）。

## Slack に記事の紹介を貼る（`meta -copy`）

非公開チームの esa は Slack で URL が展開されない。`esa meta -copy <番号|URL>` は、記事の紹介カードを
HTML とテキストの 2 形式で**同時に**クリップボードへ入れる。Slack のように HTML を受け取る貼り先ではタイトルがリンクになり、
ターミナルなどテキストしか受け取らない貼り先では最終行に URL が付いた形が貼られる。

```
📄 TUIアプリはいいぞ                                  ← HTML ではタイトルがリンク
プロダクト開発部/Tips / @koji_kawaguchi / 2026-09-28   ← カテゴリ / 作成者 / 更新日 / タグ
> topコマンドみたいやつがTUIアプリと呼ばれているのですが、…   ← 本文の冒頭 120 文字（Markdown の記法は落とす）
https://<team>.esa.io/posts/33015                     ← テキスト版だけ
```

```sh
esa meta -copy https://<team>.esa.io/posts/28025#comment-1   # # や ? 付きの URL もそのまま渡せる
esa meta -copy-title 28025                                    # タイトルのリンクだけ（「タイトル URL」）
```

- 本文の冒頭はコードブロック・表・HTML・区切り線を飛ばして作る。無い要素（タグ・本文）の行は出さない
- WIP の記事はタイトルの前に `[WIP] ` が付く。コピーした内容は stdout にも出る
- `-copy` と `-copy-title` はどちらか一方だけ。`-json` / `-comments` とは併用できない（rc=2）
- クリップボードへの書き込みには `osascript` を使う（`pbcopy` はテキストしか入れられないため）

## リリース

1. `main` の CI（gofmt / build / vet / `test -race`）が緑であることを確かめてから tag を打つ（`git tag -a vX.Y.Z -m "..." && git push origin vX.Y.Z`）
2. `curl -sL https://github.com/jiikko/esa-cli/archive/refs/tags/vX.Y.Z.tar.gz | shasum -a 256` の値で、
   [jiikko/homebrew-tap](https://github.com/jiikko/homebrew-tap) の `Formula/esa.rb` の `url` と `sha256` を更新して push する
3. **手元で入れ直して疎通を確かめる**（tag・tap・ソースからのビルドのどれかが壊れていても、ここまで来ないと分からない）

```sh
brew update
brew uninstall jiikko/tap/esa
brew install jiikko/tap/esa
readlink -f "$(command -v esa)"   # 新しい版の Cellar を指しているか
esa meta <記事番号>                   # 認証（Keychain → Cookie）と記事の取得
esa meta -copy <記事 URL>             # 紹介カードのコピー（クリップボードに HTML とテキストが入る）
ls ~/Library/Caches/esa-cli/extract   # 空であること（Cookie DB の一時コピーが残っていない）
```

## スクリプト・自動化から使う

典型的な流れは「検索して番号を得る → 本文を読む」で、AI エージェントやシェルスクリプトからも同じ:

```sh
esa search 'キーワード'   # まず絞り込む → 番号を得る（必要な列だけ -c で）
esa show <番号>           # 本文を Markdown で読む
```

- 出力をパースするなら `-json`、TSV で十分なら `-no-header`。
- 全体像は `esa --help`、各コマンドの詳細は `esa <サブコマンド> --help`。
