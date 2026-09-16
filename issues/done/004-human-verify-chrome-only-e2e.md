# 004 human: Chrome 専用化の E2E 動作確認（実ログインセッションが要る）

カテゴリ: human / 期限: 2026-09-23

issue 003（Chrome 専用化）で、私が**実行できなかった確認**をここに起こす。
いずれも Chrome の esa ログインセッションと macOS Keychain の解錠が要るため、
セッション内では実行せずユーザーに委ねた（CLAUDE.md の方針どおり、応答本文に書いて流さない）。

003 でどこまで実測したかは [`003-refactor-chrome-only.md`](003-refactor-chrome-only.md) の
「結果（実測）」節にある。**認証が要らない経路は実測済み**（`-browser` が rc=2 で弾かれること、
`esa config set browser` が rc=2 になること、各 `--help` に `browser` の語が 0 件であること、
変異 4 本のケース単位の red）。ここに残っているのは認証が要る経路だけ。

## 確認してほしいこと

- [x] `esa search 'なにかキーワード'` が従来どおり結果を返す
      （プロファイルの自動検出が `listChromeProfiles` の改名後も動くこと）
- [x] `esa show <番号>` が本文を返す
- [x] 自動検出のキャッシュが新しい名前で作られる
      → `ls ~/.config/esa-cli/cache/` に `profile-<team>` ができていること。
      旧 `profile-Chrome-<team>` が残っていても**害は無い**（読む経路はコードに無い）。
      邪魔なら手で消してよい
- [x] `esa setup` を最後まで通し、ブラウザを尋ねる質問が消えていること・
      ステップが「チーム名 → プロファイル番号 → 保存」の 3 段で違和感が無いこと
- [x] `esa config` の表示に `browser:` の行が無く、`team` / `profile` だけが出ること

## 🚨 事前に控えてほしいもの

`esa setup` / `esa config init` / `esa config set` は config.yml を `profile` / `team` から
**組み立て直す**ので、手書きのコメント行や未知のキー（`browser:` を含む）が消える。
これは以前からの挙動だが、`browser` は「かつて正式なキーだった値」として初めてこれに当たる。

```sh
cp ~/.config/esa-cli/config.yml ~/.config/esa-cli/config.yml.bak   # 実行前に 1 回
```

## 期待しない挙動が出たら

- 「Cookie を持つ Chrome プロファイルが見つかりません」が出る場合、**config.yml に
  `browser: Brave` 等が残っていて Chrome では esa にログインしていない**可能性がある
  （廃止キーは無言で無視され Chrome が使われる。ユーザー了解済みの副作用）。
  Chrome でログインし直すか、`profile` を明示指定する
- 🚨 **より紛らわしい形**: 同じマシンの Chrome でも esa に**別アカウント**でログインしていると、
  自動検出がそれを黙って拾う。「設定と違う identity で読んでいた」と気づいたら、
  003 の「対応せず記録に留めたもの」1 番（廃止キーの検出を stderr に出す案）の
  **再評価の trigger** に当たるので、その旨を報告してほしい

## 結果（2026-09-16 実測。ユーザーが config.yml をバックアップしたうえで実行）

ユーザーの Chrome セッションを使って、上の全項目を実機で確認した。**すべて期待どおり**。
社内文書の中身は転記せず、件数・形・ストリームだけを見ている。

| 確認 | 結果 |
|---|---|
| `esa search 'esa'` | rc=0 / stdout 16 行（ヘッダ + 15 件）/ **stderr 0 行** |
| `esa show <番号>` | rc=0 / stdout 62 行 / stderr 0 行。front matter のキーは `title` `category` `tags` `created_at` `updated_at` `published` `number` |
| `esa meta <番号>` | rc=0 / stdout 10 行 / stderr 0 行 |
| `esa revisions <番号>` | rc=0 / stdout 20 行 / stderr 0 行 |
| `esa config` | rc=0 / stderr 0 行。`team` と `profile` のみ表示、**`browser:` の行は無い** |
| `esa setup`（非対話） | rc=0 / stderr 0 行。**ブラウザを尋ねる質問は 0 件**、ステップは「チーム名 → プロファイル番号 → 保存」の 3 段。書き戻された config.yml はバックアップと **diff なし** |

### 自動検出のキャッシュ

🚨 **`profile` を固定していると自動検出の経路自体を通らない**ので、この項目は
`-profile auto` を明示しないと確認できない（`resolveProfileClient` は
`cfg.profile != "" && cfg.profile != profileAuto` で早期 return する）。

`esa search -profile auto 'esa'` で経路を通したところ:

- stderr に `esa: ログイン済みプロファイル "Profile 3" を自動検出しました` が出た
- `~/.config/esa-cli/cache/` に **`profile-ubiregiinc`（新名）が新規作成**された
- 旧名 `profile-Chrome-ubiregiinc`（9/14 作成）は孤児として残存。issue 003 の記述どおり
  実害は無い（読む経路がコードに存在しない）。消したければ手で消してよい

### 🚨 測り方でハマった点（記録）

`esa search --help 2>&1 >/dev/null | wc -l` が **57 行**を返し、「stderr にも出ている」と
一瞬読み違えた。原因は**実装ではなく測り方**で、このマシンの zsh は `MULTIOS` が on のため
`2>&1 >/dev/null` が stdout を両方へ流す（同じコマンドを bash で実行すると **0 行**）。
ファイルへ分離して測れば stderr は **0 バイト**。

`measure-external-cli-streams-separately` の「`2>&1` を通した観測を実測事実として扱わない」が
そのまま当てはまる事例。**ストリームの判定に `2>&1` を使わないこと。**

## 残タスク

- なし（全項目を実機で確認済み）
- 主観的な「違和感が無いか」の最終判断だけはユーザーに委ねる。上の `esa setup` の
  対話ログを応答で提示済み。
