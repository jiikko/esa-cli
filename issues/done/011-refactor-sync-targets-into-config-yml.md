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
  - **値が変わらないキーには触らない**（アンカーを壊さない・空の `profile:` の上のコメントを消さない）。キーを消すときは、キーに付いたコメントを次のキーか末尾へ移す（最初のキーの上のコメントは、yaml.v3 ではファイル先頭のコメントと区別できないため、次のキーの説明として付く）。
  - 書いた内容を読み戻し、書こうとした profile / team になっているかを確かめる（`<<:` のマージで入っている値は消せないので、ここで止まる）。
- 読めない `config.yml`（壊れた YAML・`---` で区切った複数の文書・最上位がマッピングでない・重複したキー・`team: [a]` のように `loadFileConfig` で読めない形・最上位の `targets:`）には、どちらも書かず、`esa sync` も読まない。
  以前の `setup` / `config init` は、壊れていても struct から書き直していた（中身が消えた）。
- `config.yml` がシンボリックリンクなら、リンクを残して実体へ書く（006 の `sync.yml` の扱いを共通化）。アトミックに置き換える。既存のファイルの権限は保ち（新規は 0600）、読み取り専用のファイルには書かない。最上位は常にブロック形式で書く。
- **v0.1.8 までの `sync.yml` は読まない**。残っていれば `esa sync` / `esa sync add` / `esa sync list` がエラーで移し方を案内する（両方を読むと、正本がどちらか分からなくなるため）。
- 書き戻すと YAML の整形（字下げ 2・引用符・CRLF → LF・BOM を外す・`<<:` → `!!merge <<:`）はそろう（006 の `sync.yml` の追記と同じ）。

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
- [x] 敵対的レビュー（opus・read-only、2 周） — commit「fix(config): config.yml の書き戻しでコメント・重複した sync: を取りこぼす形を直す」「fix(config): 最後のキーを消した config.yml が flow 形式に詰まる形と、読み取り専用のファイルの上書きを直す」
- [x] 変異検証 17 本すべて red（使い捨ての worktree）: 旧来の組み立て直しに戻す / sync.yml を見ない / 複数文書を許す / 空の値でキーを消さない / リンクを解決しない / sync 追記で他のキーを落とす / 読み戻しの検査を外す（最初は緑 → マージのケースを足して red）/ 値が同じでも書き換える / 消すキーのコメントを移さない / 読み込みの検査を外す / targets を許す / BOM を外さない / 権限を保たない / flow 形式を解かない / 空のときコメントを先頭へ回さない / 読み取り専用でも書く
- [x] push
- [x] リリース v0.2.0（2026-09-29。homebrew-tap を更新し、brew uninstall → install → esa meta / meta -copy / esa sync list / 一時コピーの残骸なし を確認）

## 敵対的レビューの結果（2026-09-29）

- 1 周目: P1 2 件（空の `profile:` を消して先頭のコメントまで消す・重複した `sync:` の 2 つ目以降を黙って読まない）→ 直した。P2 採用: sync.yml を貼った `targets:`、BOM、アンカー、権限。
- 2 周目: P1 なし（読み戻しの検査が、変えるべき値が変わらない形をすべて止めた）。P2 採用: キーが 1 つだけのファイルで消すと flow 形式に詰まる。P3 採用: 読み取り専用のファイルの上書き、エラー文の順序。
- 3 周目は回さない: 2 周目の修正（flow 形式を解く・空のときのコメントの付け替え・読み取り専用の拒否）は、それぞれ直接のテストと変異で確かめた。新しい判定は「書けない権限なら止める」だけで、止める側に倒れる。

## 受容したリスク（直さない）

- `esa config set` と `esa sync add` の同時実行、`setup` / `config init` が起動時に読んだ値を数分後に書く間の他の変更は、後から書いた方が勝つ（sync: は書く時点で読み直すので消えない）。
- ハードリンクの config.yml は、書き戻しでリンクが切れる。ディレクトリが書き込みできない（ファイルだけ書ける）と書けない（アトミックな置き換えを優先）。
- 誰でも書ける権限（0666 等）はそのまま保つ（以前の os.WriteFile と同じ）。
- `<<:` のマージで入る `targets:` は拒否しない。UTF-16 のコメントだけのファイルは既定のヘッダに置き換わる。`team: !!binary …` を同じ見た目の値で保存するとエラー文が原因違いを案内する。いずれも手で書かない形。
- ファイルの `team:` が壊れていると、`ESA_TEAM` で team を渡していても `esa sync` が使えない（以前は使えた。`loadFileConfig` は以前から警告していた）。
- 最上位の未知のキー（`sinc:` のような打ち間違い）は拒否しない（廃止した `browser:` のように残す方針のため）。

