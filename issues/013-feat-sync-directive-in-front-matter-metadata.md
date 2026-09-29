# 013 (feat): 書き出し先の指定を skill の front matter の `metadata.esa-sync` にも書けるようにする

起票日: 2026-09-29

## 概要

issue 010 の書き出し先の指定（本文の最後の行の `<!-- esa-sync: パス -->`）は、書き出すときに取り除く。
そのため esa の記事とローカルのファイルが一致せず、ローカルのファイルを見ても書き出し先が分からない。

skill（`SKILL.md`）には front matter がある。Claude Code の公式ドキュメント（https://code.claude.com/docs/en/skills.md、2026-09-29 に確認）には次のように書かれている:

- `metadata`: "Free-form YAML map for your own key-value data … read by your own tooling from `SKILL.md`. Claude Code doesn't act on its contents"
- 仕様に無いキー: "Claude Code ignores a field it doesn't recognize without reporting an error"。ただし claude.ai へのアップロードと Skills API では hard error になる（`metadata` は許可されている）
- "Claude Code reads the frontmatter only when the opening `---` is the file's first line"

そこで、指定を `metadata` の下に書けるようにする。front matter は本文の一部なので、取り除かずにそのまま書き出す。

```yaml
---
name: foo
description: ...
metadata:
  esa-sync: foo/SKILL.md
---
```

## 仕様

- front matter: 本文の 1 行目がちょうど `---` で、その後に `---` だけの行があるとき、その間を YAML として読む
- `metadata.esa-sync` が文字列なら、それを書き出し先の指定にする（パスの規則は 010 と同じ。記事のカテゴリのディレクトリからの相対）
- 書き出す本文は記事の本文のまま（指定を取り除かない）
- 末尾のコメントの書き方（010）は互換のため残す。**両方あればエラー**
- 黙って記事名の規則に戻さない（010 と同じ方針）。次はエラーにする:
  - `metadata.esa-sync` が文字列でない・空
  - `metadata` の外（最上位など）に `esa-sync` / `esa_sync` / `esaSync` などのキーがある
  - 閉じの `---` が無い・1 行目が `---` だけでない（BOM・後ろの空白）・YAML として読めない・中に `---` の行があって文書が 2 つになる front matter の中に、指定らしい行がある
  - `metadata.esa-sync` が 2 つある
- 「指定らしい」の判定は 2 つ:
  - YAML として読めるとき: キーが区切り（`-` `_` `.` 空白）を除いてちょうど `esasync`（`esa-sync` / `esa_sync` / `esaSync` / `ESA sync`）**かつ値が `.md` で終わる**
  - 読めない・閉じの無いとき: 行の中に「esa〜sync の直後にコロン」と `.md` がある（コメントの行 `<!--` は 010 の検査に任せて見ない。閉じの無いときは最初の空行まで）
  - 値を条件に入れるのは、水平線の `---` で始まる普通の記事・`esa sync: 書き出しのコマンド` のような説明の文を止めないため（010 の崩れの検出も「.md のパスが続く」を条件にしている）
- 値がエイリアス（`*p`）なら解決して読む
- **結果の側の注意**（red team 2 周目。字面の検出が収束しないため、軸を結果へ移した）: front matter に `description` がある記事なのに指定が無く、
  `<ディレクトリ>/SKILL.md` 以外へ書くときは、dry-run と `--apply` の出力に「注意:」を出す（変更なしの記事にも毎回）。エラーにしないのは、`.claude/agents/*.md` のように
  description を持つ skill 以外のファイルがあるため。そうしたファイルは `metadata.esa-sync` に自分のパスを書くと注意が消える（文言で案内する）
- 注意も出ない形: YAML が読めない front matter で、かつ字面の検出にも当たらない書き損じ（書き損じが 2 つ重なったとき）
- 検出しない形（上の注意で気づく）: 複合キー（`? [esa-sync]`）・語が崩れたキー（`esa2sync` / `esa-syncs`）・値がシーケンスのもの・
  metadata の子が 1 つだけでコロンの後の空白を落としたもの（metadata の値が 1 つの文字列になる）。merge key で metadata へ入れた指定は誤検出で止まる（受容）
- 010 の `--` を禁じる規則は HTML のコメントの事情なので、コメントの側だけに残す

## 対応方針

- `cmd/esa/sync.go` に front matter から指定を読む関数を足し、`mapSyncFiles` はコメントの指定と front matter の指定をまとめて探す 1 つの関数を呼ぶ
- help と README の「書き出し先の指定」を front matter を先に書き直す

## 関連ファイル

- `cmd/esa/sync.go` / `cmd/esa/sync_directive_test.go` / `README.md`
- issue 010（末尾のコメントの指定）

## 進捗

- [x] 実装とテスト（`cmd/esa/sync_frontmatter.go` / `sync_frontmatter_test.go`。`mapSyncFiles` は `findSyncDirective` を呼ぶ）
- [x] 変異で red を確認（使い捨ての worktree で 4 周・計 28 本。下）
- [x] 反証レビュー（壊す 3 周 + 回帰と文書 1 本。下）
- [x] help / README（issue 010 の `--` の記述にも追記）
- [ ] リリース（未。リリースするまで esa の記事を新しい書き方へ直さない。今の v0.2.0 は metadata を読まず、記事の題のファイル名で書き出す）

### 変異（2026-09-29）

1 周目 8 本（metadata の外のキーを見逃す・front matter の指定を無視する・両方の併用を通す・閉じ忘れを通す・コメントの `--` を通す・値の型を見ない・重複を通す・壊れた YAML を通す）はすべて red。
2 周目 7 本（語に分けずに esasync を含むかで判定する・値の .md を条件にしない・1 行目の崩れを見ない・文書が 2 つを通す・エイリアスを解決しない・パスの検査に `--` を戻す・壊れた YAML の行のキーを見ない）はすべて red。
最後の 1 本は最初は緑だった（壊れた YAML に指定と関係の無い `キー: x.md` の行があるケースが無かった）。ケースを足して red を確認した。

### 反証レビュー（2026-09-29）

- ①壊す（opus）: 採用 P1 1 件・P2 3 件・P3 5 件
  - P1: 水平線で始まる記事に 010 のコメントの指定を書くと「閉じの --- がありません」で止まる（010 の回帰）
  - P2: 閉じの無い `---` で始まる記事の英文（`processes async`）・`uses_async` のようなキー・水平線に挟まれた `esa sync: …` の文で止まる
    → 判定を「語に分けて esa と sync が並ぶ」かつ「値が .md で終わる」に変えた
  - P3: 壊れた YAML とコメントの指定の併用が止まる（→ 同上）。BOM・`--- `・`---\t` の 1 行目、中の `--- ` で文書が 2 つになるものが黙って戻る（→ エラー）。値のエイリアス（→ 解決して読む）。
    テストが守っていない分岐 2 つ（front matter で `--` を通すこと → テストを追加。パスの複製の変異 → 今の作り方では等価のため記録だけ）
  - 採らない: 複合キー `? [esa-sync]`（現実の書き損じではない。「検出しない形」に記録）
- ③壊す 2 周目（opus）: 採用 P2 2 件・P3 2 件
  - P2: metadata の子が 1 つで `esa-sync:a/SKILL.md`（コロンの後の空白なし）・全角コロンが黙って戻る → 字面で塞がず、結果の側の注意で知らせる
  - P2: 壊れた・閉じの無い front matter の中の、行末のコメント・フロー形式・空白入りのパスの指定が黙って戻る → 行の判定を「esa〜sync と .md を含む行」へ緩めた
  - P3: 水平線に挟まれた `詳しくは esa sync: README.md` が止まる → キーの判定を「区切りを除いてちょうど esasync」に。閉じの無いときの解説記事の例 → 最初の空行までに絞った
  - P3: 値がシーケンス・語の崩れ・merge key → 「検出しない形」に記録（注意で気づける）
  - 直しで出た回帰（テストで検出）: 閉じの無いときの走査が 010 のコメントの指定の行を拾う → `<!--` の行を外した
  - 変異 3 周目 8 本（注意を出さない・SKILL.md でも出す・description を見ない・変更なしで出さない・キーの部分一致・空行で切らない・コメントの行を見る・.md を条件にしない）はすべて red
- ④壊す 3 周目（opus）: P1 なし。採用 P2 1 件・P3 4 件
  - P2: カテゴリ直下の `SKILL.md`（ディレクトリなし）で注意が出ない → 条件を `<ディレクトリ>/SKILL.md` に
  - P3: name の無い skill で注意が出ない → description だけを見る。agents で毎回鳴る → 消し方を文言で案内。
    壊れた YAML の中の説明の文（`esa sync の使い方: README.md`）で止まる → 行の判定を「esa〜sync の直後にコロン」に。
    テストが守っていない分岐（指定のある記事に注意を出す・小文字の skill.md）→ テストを追加
  - 記録のみ: 書き損じが 2 つ重なる形（上の「注意も出ない形」）。`len(docs)` と MappingNode の検査を外す変異は今の作りでは等価
  - 変異 4 周目 5 本はすべて red
  - **周回はここで打ち切る**: 3 周目は P3 中心で P1 なし、直しは条件を絞る小さな変更で、各々を変異で直接確認した
    （`~/.claude/rules/adversarial-review-own-safeguards.md` §8 の打ち切り条件。字面の検出は「検出しない形」を書いたうえで結果の側の注意に頼る）
- ②回帰と文書（sonnet）: 回帰なし（`--` の検査はコメントの経路で同じ値・同じ時点に止まる）。採用: issue 010 の `--` の記述が古い（→ 追記）、front matter で `--` を通すことのテストが無い（→ 追加）
