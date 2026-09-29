# 012 (test): TestSyncDoesNotLeakDescriptorsOrGoroutines が flaky（2 通りの落ち方）

起票日: 2026-09-29

## 概要

`cmd/esa/sync_test.go` の `TestSyncDoesNotLeakDescriptorsOrGoroutines`（`esa sync` を 100 回まわし、
fd と goroutine の増加を見る検査）が、コードを変えなくても落ちることがある。落ち方は 2 通りあり、
どちらも HTTP の接続の使い回しの問題だった（下の「進捗」で実測した）。

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

- [x] 1 の原因の特定: 前後の `lsof` の差分で増えていたのは、プロセス内の fake サーバとの `ESTABLISHED` の TCP 接続
  （keep-alive で待機中の接続。クライアント側とサーバ側の 2 本ずつ）。プールに何本待機しているかで数が揺れていた
- [x] 2 の原因の特定: 全件失敗した回のエラーは `dial tcp 127.0.0.1:…: connect: can't assign requested address`
  （一時ポートの枯渇）。記事の取得は 6 並列だが、`http.DefaultTransport` のホストごとの待機接続は 2 本まで
  （`MaxIdleConnsPerHost` の既定）なので、残りは毎回閉じて張り直していた。閉じた接続の TIME_WAIT が `-count` の回を
  重ねて積もり、ポートが尽きた。**本番でも**、sync・search の補完で記事ごとに接続を張り直していた
- [x] 修正（commit「fix: 記事の並列取得で接続を使い回し、sync の漏れ検査を待機中の接続で揺らさない（issue 012）」）
  - 本番: `cmd/esa/client.go` に全クライアント共有の `sharedTransport`（`DefaultTransport` の Clone、
    `MaxIdleConnsPerHost = fetchConcurrency`）を置く。並列度を `fetchConcurrency` 定数にまとめ、search の補完と
    sync の取得の両方がそれを使う（並列度だけを上げて使い回しが外れる形にならない）
  - テスト: fd を数える前に `sharedTransport.CloseIdleConnections()` し、数が落ち着くまで待つ（待機中の接続だけを
    閉じるので、使い中のまま漏れた接続は残る）
- [x] 確認: `-count=30` を 3 回（90 回）で失敗 0（修正前は 30 回中 2 回前後）。go1.26 / go1.25.0 で全テスト緑
- 変異: 待機接続を既定の 2 本に戻す → `-count=30` で「--apply がほとんど成功していない（0 回）」が再現（red）/
  書き出し先の `root.Close` を外す → `fd 10 → 80` で red（漏れの検出力は残っている）
- 却下した変異: 応答の body の `Close` を外す変異は、修正前のテストでも緑。fake サーバの失敗応答は body が空で、
  EOF まで読めた接続は Close しなくてもプールへ戻るため、漏れになっていない（等価変異）
- 反証レビュー（read-only のサブエージェント）済み: 候補に接続プールを足し、ロックのファイルを候補から外した
