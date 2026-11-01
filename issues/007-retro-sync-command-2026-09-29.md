# 007 retro: esa sync の実装（issue 006）の振り返り

起票日: 2026-09-29

## どこで踏んだか / 次に効きそうな改善

1. **`go get <module>@latest` が go.mod の `go` 行（1.25.0 → 1.26.0）と無関係な依存まで上げた**
   - 配布（Homebrew の build）に効く変更が、依存を 1 つ足しただけで混ざる。差分を見て気づいて戻し、依存グラフに既にある版を指定し直した
   - 一般形: 依存を足すときは `@latest` ではなく、まず `go mod graph` に既にある版を指定する。足した後は `git diff go.mod` の `go` 行と、direct の依存の版を見てから commit する
   - 切り出し先の提案: dotfiles の `_claude/rules/` の既存ルールへの追記（Go 固有なので、追記先が無ければ却下）

2. **外部の名前をファイル名に写す処理で、ファイルシステムの同一性（大文字小文字・NFC/NFD・case folding）を最初の設計で考えていなかった**
   - バイト比較の衝突検査は、APFS では同じファイルになる 2 記事を通し、片方を黙って消した。ToLower でも ß/ss・σ/ς・Cherokee がずれた。
     敵対的レビューで 2 周かかり、最後は本物の APFS を正解役にした全組の照合（15,623 組）で閉じた
   - 一般形: 外部から来た名前（記事名・ユーザー名・URL の断片）をパスに写すときは、**書き先のファイルシステムが同一視する規則で鍵を作って**衝突を検査する。
     規則の近似を書いたら、最初から本物のファイルシステムを正解役にして網羅的に突き合わせる
   - 切り出し先の提案: dotfiles の `adversarial-review-own-safeguards.md` §0-B（近似は本物を正解役に網羅的に突き合わせる）への実例の追記

3. **dotfiles の `bin/mutate-verify` が dotfiles の外の repo で動かない**（repo root 相対で `scripts/lib/` を読む）
   - 同じ前提検査（baseline・当たったか・構文・狙った FAIL）を持つ使い捨てのハーネスを手で組み直した
   - 切り出し先の提案: dotfiles に新規 issue（他の repo でも使えるようにする）

## 決着（2026-11-01）

- 項目 1: 採用。017 の項目 1 と同じ件なので 1 本にまとめ、dotfiles の `_claude/rules/verify-execution-not-just-exit-code.md` の toolchain の節に追記した（dotfiles commit「rules: esa-cli retro 007 / 017 の提案を追記する」）
- 項目 2: 却下（既存の規則で吸収済み）。`measure-external-cli-streams-separately.md` の「外部の道具の意味論を自分で書き直すなら、本物を oracle にして差分の検査を作る」と、`~/.claude/CLAUDE.md` の「在るローカルのファイルどうしは実体 (`(dev, ino)`) で比べる」が同じ一般形を持つ
- 項目 3: 解消済み。dotfiles issue 585（mutate-verify が dotfiles の外で動かない）が done で、`mutate-verify` はどの repo からも呼べる

## 残課題

- なし
