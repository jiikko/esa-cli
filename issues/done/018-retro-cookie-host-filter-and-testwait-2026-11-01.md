# 018 (retro): Cookie を送信先で絞る変更と、testwait の module 名で go mod tidy が落ちた件の振り返り

起票日: 2026-11-01

## 概要

chromecookie の読み取りを送信先ホストで絞り（esa-cli / slack-cli / newrelic-nrql-cli へ展開し、ReadCookies を削除）、その取り込みで表に出た「取り込む側の go mod tidy が落ちる」を dotfiles の lint（`scripts/go_module_graph.sh`）で止めるまでのセッションの振り返り。

## どこで踏んだか

1. **終了コードの取り違えで「全 module で tidy の差分なし」と誤報告した**。`echo "$(basename $d) tidy_rc=$?"` の `$?` は、同じ行のコマンド置換（`basename`）の終了コードを拾う。その後に回した各 module の `make lint`（`go mod tidy -diff` を含む）で、testwait を使う 17 module のうち 14 が赤になって初めて気づいた（chromecookie・doctor・glogx・dupscan・mac-lan-manager・pro-con・ratelimit・restartable・runtimeout・serialrun・schedkeys・tuikit・zundamon-kaisetsu・treefiler。require ブロックの並び順の差分だけ）。規則は既にある（`measure-external-cli-streams-separately.md`「rc は同じ行のコマンド置換で壊れる。先に変数へ退避してから使う」）。
2. **module 名の改名を push する前に、root の `make test-lint` を回さなかった**。各 module の `make lint` / `make test` は回したが、repo 全体の索引 `src/PACKAGES.md`（`scripts/go_packages_index.sh` の生成物）が古いまま push され、CI の Lint（`test-go-packages-index`）が赤になった。規則は既にある（`verify-execution-not-just-exit-code.md`「関係しそうな suite だけを選んで回すのも代用」）。
3. **ライブラリ（chromecookie）を変えて取り込む側へ配ったが、取り込む側の立場での確認（`go mod tidy`）を最初は回していなかった**。build / test / CI は通るので、ドット無しの module（testwait）を依存に持つことが見えなかった。気づいたのは go.sum の古い行を消そうとして tidy を打ったとき。dotfiles の中では replace が穴を埋め、lint の `go mod tidy -diff` / `go list -m all` も緑だった。
4. **新設した検査（go_module_graph.sh の節）は、1 回目の版に対する敵対的レビューで、採った指摘が 4 点あった**（ゼロ埋めの pseudo-version / 固定した commit と作業ツリーの食い違い / ドット付きでも取得できない名前 / stderr の混入。どれも c4d94f3a7 のテストのケースになっている）。直した版も 2 周目で固定した commit の go.mod の require を見ていない穴が出た。最初の版は「名前と版の文字列の形」だけを見ており、取り込む側が実際に読むもの（その版の commit の go.mod）を見ていなかった。

## 何が回りくどかったか

- 3 の根本原因（外から取り込まれる module が replace でしか解決できない依存を持つ）に着くまでに、症状への対処（go.sum の手での整理・testwait の改名・pin）を先に 2 回入れた。ユーザーに「本質的な解決」を求められてから検査を作った。
- testwait の改名と pin が 2 commit に分かれた（pin は改名の commit の pseudo-version を指す必要があるため）。これは Go の仕組み上避けられない。

## 次に効きそうな改善（提案）

- **A. 別の repo・利用者が取り込む成果物（ライブラリ・パッケージ・設定のテンプレート）を変えたら、配る前に、受け手の環境（取り込む側の repo・空の環境）で依存の解決と検査を 1 回回す**。作る側の repo の中では、ローカルの上書き（Go の replace・workspace・ローカルのキャッシュ・相対パスの参照）が受け手との違いを埋めるので、作る側の検査は全部緑のまま通る（今回は取り込む側の `go mod tidy` だけが落ちた）。
  - 切り出し先: `_claude/rules/verify-execution-not-just-exit-code.md` の「隔離環境での成功は、本番での成功ではない」節への 1 項の追記。新しい rule にはしない（発動点は「作ったものを別の場所で使う」で、同じ節が扱っている）。
  - 判断: 採用 (2026-11-01)。dotfiles の commit「docs(rules): 取り込まれるものを変えたら、受け手の環境で依存の解決と検査を回す (esa-cli retro 018)」で、
    `_claude/rules/verify-execution-not-just-exit-code.md` の該当節に 1 項を足し、実例を同名の rules-rationale へ置いた。

## 却下

- 4（検査の入力を、値の文字列ではなく相手が実際に読むものにする）は提案にしない: `adversarial-review-own-safeguards` のチェックリスト §0-B（本物の経路を正解役にする・近似が再現していない部分を列挙する）と同じ内容で、言い換えにしかならない（反証レビューの指摘）。

- 1 と 2 は新しい規則にしない: どちらも既存の規則（上に引いた行）がそのまま当てはまり、適用し損ねただけ。規則を足しても同じ読み落としは防げない。2 は、検査を repo 全体の `make test-lint` に寄せていれば機械で止まった（dotfiles の pre-push は Go の lint を回さない）が、pre-push に重い lint を足すのは別の判断なので、ここでは提案しない。

## 進捗

- [x] A の切り出し（上記の commit。make test-lint rc=0、常時読み込みの指示ファイルの合計 108,628 字で上限 150,000 の内）
- 残課題なし（提案は A を採用、4 と 1・2 は却下に理由つきで記録）
