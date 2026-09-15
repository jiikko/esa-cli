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

- [ ] `.github/workflows/ci.yml` を追加し、push / PR で以下が走る
  - [ ] `gofmt -l .` の出力が空（整形差分ゼロ）
  - [ ] `go vet ./...`
  - [ ] `go build ./cmd/esa`
  - [ ] `go test ./...`
- [ ] **検査が実際に走った証拠を CI ログで確認する**（緑だけで判断しない。各ステップの出力が出ていること）
- [ ] **意図的に壊す変更を 1 つ当てて CI が赤くなることを確認する**（検査が退行を検出できる証拠）
- [ ] テスト対象の選定と実装（下記「テストできる範囲」）

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
