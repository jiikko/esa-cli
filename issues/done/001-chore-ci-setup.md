# CI を整備する（GitHub Actions）

起票日: 2026-09-15

## 背景

このリポジトリには CI が無く、`gofmt` / `go vet` / `go build` は手元で回しているだけ。
壊れた状態が push されても気づく機構が無い。

### 現状（2026-09-15 実測）

- `.github/` が存在しない（workflow 無し）
- `*_test.go` が **0 件**（ユニットテストが無い）
- `GOOS=linux GOARCH=amd64 go build ./cmd/esa` は**通る**（＝ ubuntu ランナーでビルド検査が可能）
- 実行時は macOS 専用（Keychain での cookie 復号・Chrome の DB パスに依存）

## やること（受け入れ条件）

- [x] `.github/workflows/ci.yml` を追加し、push / PR で以下が走る
  - [x] `gofmt -l .` の出力が空（整形差分ゼロ）
  - [x] `go vet ./...`
  - [x] `go build ./cmd/esa`（`go build ./...` で覆う）
  - [x] `go test ./...`（`-race` 付き）
- [x] **検査が実際に走った証拠を CI ログで確認する**（緑だけで判断しない。各ステップの出力が出ていること）
- [x] **意図的に壊す変更を 1 つ当てて CI が赤くなることを確認する**（検査が退行を検出できる証拠）
- [x] テスト対象の選定と実装（下記「テストできる範囲」）

## 設計メモ

### ランナー

- ビルド・vet・gofmt は **ubuntu-latest で十分**（Linux クロスビルドが通ることを確認済み）
- 実行時挙動は macOS 依存なので、`macos-latest` を matrix に足すかは要判断。
  ただし **cookie / Keychain を要する経路は CI では実行できない**（認証が無い）ので、
  macOS ランナーを足しても検査できるのは「ビルドが通る」までで、費用対効果は低い

### テストできる範囲（cookie もネットワークも不要な純粋ロジック）

CI の実効性はここに懸かっている。テストが 0 件のままでは workflow を足しても
「ビルドが通る」以上のことを守れない。

- `cookieHostMatches` — Cookie ドメインのドット境界判定（`notesa.io` を誤って一致させない）
- `buildCookieHeader` — 同名 Cookie の解決（host-only を domain より優先）
- `pkcs7Unpad` — 不正パディングを弾く
- `decryptValue` — `meta_version >= 24` のハッシュプレフィックス 32 バイト除去（鍵は固定値でよい）
- `parseColumns` / `columnsNeedEnrich` — 不明カラムのエラー、enrich 要否の判定
- `dateOnly` — ISO8601 → `YYYY-MM-DD`
- `parseSearchHTML` — 固定 HTML の fixture から number / title を抽出。
  **「0 件」と「抽出失敗（セレクタ変更）」を区別できること**を必ず検査する（`errParseFailed`）
- `applyPostJSON` — HTML エンティティの復元（`&#47;` → `/`、`&#35;` → `#`）を含む
- `parseNumberArg` — 番号 / URL / 不正入力

### CI で検査**しない**と決めること（明記しておく）

- Chrome cookie の復号、プロファイル自動検出、esa への実アクセス
  （認証情報が必要で CI では原理的に実行できない）
- Homebrew formula のインストール（tap 更新と brew の状態に依存する）

## 残タスク / スコープ外

- **リリース自動化は別 issue**: タグ push で GoReleaser を回し、tap の formula（`url` / `sha256` / `version`）を
  自動更新する。雛形は `.goreleaser.yaml` に置いてある。現状はタグ付け → sha256 取得 → formula 手動更新の 3 手
- 実アクセスを伴う動作確認は CI では不可。必要なら `human` issue として起票する

## 進捗

### 2026-09-29 完了

- commit「ci: GitHub Actions で gofmt / build / vet / test -race を回す（issue 001）」: newrelic-nrql-cli の ci.yml と同じ形。
  **runner は macOS**（設計メモの ubuntu 案から変えた）: 配布先の Homebrew が macOS でビルドするので同じ OS で検査する。
  public repo なので macOS runner も無料。paths フィルタは付けない（起動しなかった HEAD を緑と読み替えないため）
- 走った証拠（run 36447208665, 9bfa413）: `gofmt: 差分なし（20 ファイルを検査）` / `go build` が chromecookie を取得 /
  `go vet` / `go test -race` が `ok github.com/jiikko/esa-cli/cmd/esa` をログに出している
- 赤くなる証拠: 紹介カードの本文の HTML エスケープを外した commit を一時 ref（ci-canary-esa-001）に push して
  workflow_dispatch で走らせ、`--- FAIL: TestSlackCardLayoutAndEscaping` で失敗（run 36447709706）。ref は削除済み
- テストできる範囲の現状（起票時は 0 件）:
  - `cookieHostMatches` / `pkcs7Unpad` / `decryptValue` は chromecookie（dotfiles/src/chromecookie）へ移り、そちらでテストされる。
    esa 側は `chromecookie.HostMatches` の回帰表（cookies_test.go）を持つ
  - `buildCookieHeader` / `parseSearchHTML`（0 件と抽出失敗の区別は search_empty_test.go）/ `parseNumberArg` は既存のテストがある
  - `parseColumns` / `columnsNeedEnrich` / `dateOnly` / `applyPostJSON` は columns_test.go を新設。変異で red を確認
    （不明カラムを通す / enrich 判定を常に偽 / 日付を 7 文字で切る / エスケープを復元しない / タグを置き換えず追記）
- CI で検査しないと決めたこと（Chrome Cookie の復号の実行・プロファイル自動検出・esa への実アクセス・brew install）は起票時のとおり。
  brew の入れ直しと疎通は README の「リリース」節で手元の手順にした
- 残タスク: リリース自動化（GoReleaser）は起票時のとおり別 issue（未起票）
