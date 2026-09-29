# 012 (test): TestSyncDoesNotLeakDescriptorsOrGoroutines が flaky（2 通りの落ち方）

起票日: 2026-09-29

## 概要

`cmd/esa/sync_test.go` の `TestSyncDoesNotLeakDescriptorsOrGoroutines`（`esa sync` を 100 回まわし、
fd と goroutine の増加を見る検査）が、コードを変えなくても落ちることがある。落ち方は 2 通りで、原因も別。

## 詳細（2026-09-29 の実測）

### 1. fd の数が揺れて落ちる（CI でも手元でも観測）

- run: esa-cli の CI、commit 4d796da（「fix(sync): --apply の排他を…」）の 1 回目
- 出力: `sync_test.go:1184: 100 回の実行で増えた: fd 19 → 22 / goroutine 9 → 9`
- 同じ commit の failed job の再実行は緑。中身が同じでも結果が変わる
- 手元でも `-count=30` の中で出る（反証レビューの実行: `fd 12→14 / goroutine 6→9`）
- 検査は「プロセス全体の fd を `lsof -p` で数えて +2 まで」。数えているのはロックや一時ファイルに限らない。
  fake サーバ（httptest）との keep-alive 接続などの増減も入る、と見ているが、**どの fd が増えたかは未確認**
- 4d796da の変更（ロックを 2 つ取る）が漏らしていないことは別に確かめた: `acquireSyncLock` → `release` を
  500 回くり返しても fd は 9 のまま

### 2. 「--apply がほとんど成功していない」で落ちる（手元で繰り返すと出る）

- 再現: `go test -count=30 -run '^TestSyncDoesNotLeakDescriptorsOrGoroutines$' ./cmd/esa/`
- 出力: `--apply がほとんど成功していない（N 回）。漏れを測る経路を通っていない`（N は 0〜16 と大きくばらつく）
- 頻度: 変更前の f6f7692 でも変更後の 4d796da でも、30 回中 2 回ずつ（macOS / go1.26）
- 1 回だけの実行（`-count=1` を別プロセスで 10 回）では 10 回とも緑（反証レビューの実行）。`-count` で続けて回したときに
  前の回の状態が残り、前提（`applied >= 20`）が崩れると見ている
- 反証レビューの `-count=30` では、失敗の 2 回が**連続**した（20 回目が `applied 0 回`、直後の 21 回目が 1 の fd の揺れ）。
  前の回から何かが残る見立てと合う
- **残るものの最有力候補は HTTP の接続プール**: `cmd/esa/client.go` の `newHTTPClient` は `Transport` を指定せず、
  プロセス全体で共有される `http.DefaultTransport`（接続プールと、その読み書きの goroutine）を使う。
  テストは回ごとに新しい `*http.Client` と fake サーバを作るが、プールは回をまたいで生きる。1 の fd / goroutine の揺れも
  同じもので説明が付く。**実測で確かめたわけではない**
- ロックのファイルは候補から外れる: `XDG_CONFIG_HOME` と書き出し先は回ごとに `t.TempDir()` で作り直すので、
  ロックのパスは回ごとに別になる

## 対応方針（案）

- まず前後の `lsof` の差分で、増えた fd が fake サーバとの TCP 接続かを見る（接続プールの見立ての確認）
- 1: 見立てどおりなら、数える前にプールを空にする（`http.DefaultTransport.(*http.Transport).CloseIdleConnections()`）か、
  漏れを見たい fd（ロック・書き出し先・一時ファイル）だけを `lsof` の出力のパスで絞って数える
- 2: 何が前の回から残っているかを観測してから直す（`applied` の数え方・`fakeEsa` の状態・書き出し先の残り）
- 直したら、`-count=30` を複数回まわして 0 回になることを確かめる。直す前の形に戻して落ちることも確かめる

## 関連ファイル

- `cmd/esa/sync_test.go` — `TestSyncDoesNotLeakDescriptorsOrGoroutines`
- `cmd/esa/sync_lock.go` — 4d796da で鍵を 2 つ取るようにした（今回の揺れの原因ではないことは上で確認済み）

## 進捗

- [ ] 1 の原因の特定（増えた fd の中身。最有力は http.DefaultTransport の接続プール）
- [ ] 2 の原因の特定（前の回から残っているもの。同上）
- 反証レビュー（read-only のサブエージェント）済み: 引用・行・commit は一致。候補に接続プールが抜けていたのを足し、
  ロックのファイルを候補から外し、1 回だけの実行では出ないこと・失敗が連続したこと・1 が手元でも出ることを足した
- [ ] 修正と `-count=30` での確認
