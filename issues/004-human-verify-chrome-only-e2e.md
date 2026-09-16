# 004 human: Chrome 専用化の E2E 動作確認（実ログインセッションが要る）

カテゴリ: human / 期限: 2026-09-23

issue 003（Chrome 専用化）で、私が**実行できなかった確認**をここに起こす。
いずれも Chrome の esa ログインセッションと macOS Keychain の解錠が要るため、
セッション内では実行せずユーザーに委ねた（CLAUDE.md の方針どおり、応答本文に書いて流さない）。

003 でどこまで実測したかは [`done/003-refactor-chrome-only.md`](done/003-refactor-chrome-only.md) の
「結果（実測）」節にある。**認証が要らない経路は実測済み**（`-browser` が rc=2 で弾かれること、
`esa config set browser` が rc=2 になること、各 `--help` に `browser` の語が 0 件であること、
変異 4 本のケース単位の red）。ここに残っているのは認証が要る経路だけ。

## 確認してほしいこと

- [ ] `esa search 'なにかキーワード'` が従来どおり結果を返す
      （プロファイルの自動検出が `listChromeProfiles` の改名後も動くこと）
- [ ] `esa show <番号>` が本文を返す
- [ ] 自動検出のキャッシュが新しい名前で作られる
      → `ls ~/.config/esa-cli/cache/` に `profile-<team>` ができていること。
      旧 `profile-Chrome-<team>` が残っていても**害は無い**（読む経路はコードに無い）。
      邪魔なら手で消してよい
- [ ] `esa setup` を最後まで通し、ブラウザを尋ねる質問が消えていること・
      ステップが「チーム名 → プロファイル番号 → 保存」の 3 段で違和感が無いこと
- [ ] `esa config` の表示に `browser:` の行が無く、`team` / `profile` だけが出ること

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
