# 011 refactor: esa sync の対象を sync.yml から config.yml の `sync:` へ移す

起票日: 2026-09-29

## 概要

`esa sync` の対象は、006 で `config.yml` と別の `sync.yml` に置いていた。
理由は、`saveFileConfig`（`esa config set` / `config init` / `setup`）が `config.yml` を struct から組み立て直して書き戻し、手で書いたコメントや未知のキーを消すためだった。

設定ファイルが 2 つあるのは分かりにくい（ユーザーの指定: 「config.yml にまとめて欲しい」）。
そこで `saveFileConfig` を「読んだ YAML のノードのうち profile / team だけを書き換える」形に直し、sync の対象を `config.yml` の `sync:` に移す。

## 仕様

- `config.yml` の最上位に `sync:`（006 の `targets:` の中身と同じ `- name / category / dir` のリスト）を持つ。
- **書き戻しは自分のキーだけを書き換える**（`config_doc.go` の `readConfigDoc` / `writeConfigDoc` / `setMappingScalar` / `mappingValue` に寄せる）。
  - `esa config set` / `config init` / `setup` は `profile` / `team` だけを書き換える。空の値はキーごと消す（以前の omitempty と同じ）。
  - `esa sync add` は `sync:` に 1 件足す。
  - どちらも、手で書いたコメント・他のキー（廃止した `browser:` を含む）・並びを残す。
- 読めない `config.yml`（壊れた YAML・`---` で区切った複数の文書・最上位がマッピングでない）には、どちらも書かない。
  以前の `setup` / `config init` は、壊れていても struct から書き直していた（中身が消えた）。
- `config.yml` がシンボリックリンクなら、リンクを残して実体へ書く（006 の `sync.yml` の扱いを共通化）。アトミックに置き換え、権限は 0600。
- **v0.1.8 までの `sync.yml` は読まない**。残っていれば `esa sync` / `esa sync add` / `esa sync list` がエラーで移し方を案内する（両方を読むと、正本がどちらか分からなくなるため）。
- 書き戻すと YAML の整形（字下げ 2・引用符）はそろう（006 の `sync.yml` の追記と同じ）。

## 考慮した点

- `esa config set` と `esa sync add` を同時に打つと、読んでから書くまでの間の更新が片方で消えうる（read-modify-write の競合）。どちらも人が対話で打つコマンドなので、ロックは置かない（受容）。
- `loadFileConfig` は今どおり素の `yaml.Unmarshal` で `profile` / `team` だけを読み、`sync:` は見ない。`parseSyncConfig` は `sync:` の各項目の未知のキーを拒む（最上位の他のキーは見ない）。

## テスト

- `cmd/esa/config_doc_test.go`（新設）:
  - `esa config set team` の後も、`sync:`・手で書いたコメント・未知のキーが残り、`loadSyncTargets` で読める。逆向きに、`sync add` の後も profile / team が残る
  - 空の値はキーごと消し、`sync:` は残す
  - 新規作成はヘッダ付き・0600
  - 読めないファイル（壊れた YAML・複数の文書・最上位がリスト / スカラー）には書かず、1 バイトも変えない
  - `sync.yml` が残っていればエラーで案内する
- 既存の `sync_test.go` の `sync.yml` 前提のテストを `config.yml` の `sync:` に直した。

## 関連

- 006（`esa sync` の本体。`sync.yml` に分けた経緯）
- 010（書き出し先の指定。同じ日に実装）

## 進捗

- [x] 実装・テスト・ヘルプ・README
- [ ] 敵対的レビュー（read-only のサブエージェント）
- [ ] 変異検証
- [ ] push
