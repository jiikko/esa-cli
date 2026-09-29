# 012 (test): TestSyncDoesNotLeakDescriptorsOrGoroutines が flaky（2 通りの落ち方）

起票日: 2026-09-29

## 概要

`cmd/esa/sync_test.go` の `TestSyncDoesNotLeakDescriptorsOrGoroutines`（`esa sync` を 100 回まわし、
fd と goroutine の増加を見る検査）が、コードを変えなくても落ちることがある。落ち方は 2 通りで、原因も別。

## 詳細（2026-09-29 の実測）

### 1. fd の数が揺れて落ちる（CI で観測）

- run: esa-cli の CI、commit 4d796da（「fix(sync): --apply の排他を…」）の 1 回目
- 出力: `sync_test.go:1184: 100 回の実行で増えた: fd 19 → 22 / goroutine 9 → 9`
- 同じ commit の failed job の再実行は緑。中身が同じでも結果が変わる
- 検査は「プロセス全体の fd を `lsof -p` で数えて +2 まで」。数えているのはロックや一時ファイルに限らない。
  fake サーバ（httptest）との keep-alive 接続などの増減も入る、と見ているが、**どの fd が増えたかは未確認**
- 4d796da の変更（ロックを 2 つ取る）が漏らしていないことは別に確かめた: `acquireSyncLock` → `release` を
  500 回くり返しても fd は 9 のまま

### 2. 「--apply がほとんど成功していない」で落ちる（手元で繰り返すと出る）

- 再現: `go test -count=30 -run '^TestSyncDoesNotLeakDescriptorsOrGoroutines$' ./cmd/esa/`
- 出力: `--apply がほとんど成功していない（N 回）。漏れを測る経路を通っていない`（N は 0〜16 と大きくばらつく）
- 頻度: 変更前の f6f7692 でも変更後の 4d796da でも、30 回中 2 回ずつ（macOS / go1.26）
- `-count` で同じテストを続けて回したときに前の回の状態が残り、前提（`applied >= 20`）が崩れるのではないかと
  見ているが、**何が残るのかは未確認**。1 回だけ（`-count=1`）の実行で出るかも未確認

## 対応方針（案）

- 1: プロセス全体の fd の数ではなく、漏れを見たい fd（ロック・書き出し先・一時ファイル）だけを `lsof` の出力の
  パスで絞って数える。または fake サーバの接続を閉じてから数える。どちらにするかは、まず増えた fd の中身を
  `lsof` の差分で見てから決める
- 2: 何が前の回から残っているかを観測してから直す（`applied` の数え方・`fakeEsa` の状態・書き出し先の残り）
- 直したら、`-count=30` を複数回まわして 0 回になることを確かめる。直す前の形に戻して落ちることも確かめる

## 関連ファイル

- `cmd/esa/sync_test.go` — `TestSyncDoesNotLeakDescriptorsOrGoroutines`
- `cmd/esa/sync_lock.go` — 4d796da で鍵を 2 つ取るようにした（今回の揺れの原因ではないことは上で確認済み）

## 進捗

- [ ] 1 の原因の特定（増えた fd の中身）
- [ ] 2 の原因の特定（前の回から残っているもの）
- [ ] 修正と `-count=30` での確認
