// Google Chrome の Cookie を macOS Keychain 経由で復号して取り出す。
//
// 復号・一時コピーの後始末（3 段構え）・エラーの分類・プロファイルの列挙は
// github.com/jiikko/dotfiles/src/chromecookie が持つ（slack-cli / newrelic-nrql-cli と共有）。
// 直すときはあちらを直し、ここへコピーを戻さないこと。ここに残すのは esa-cli 固有の部分
// （どの Cookie を送るか・自動検出の分類 profileSkipError への写し替え・案内の文言）だけ。
//
// 🚨 このツールは Google Chrome 専用。以前は Brave / Chromium / Edge / Vivaldi も対応表に持っていたが、
// 意図的に Chrome だけへ絞った（issue 003）。対応表の値（Keychain のサービス名・Application Support
// 配下のディレクトリ名）は実機で確認しないと正しいか分からないため（chromecookie の doc も同じ方針）。
package main

import (
	"fmt"
	"strings"

	"github.com/jiikko/dotfiles/src/chromecookie"
)

// ws は esa-cli の作業領域（~/Library/Caches/esa-cli/extract）。Cookie DB の一時コピーはここを通る。
var ws = chromecookie.NewWorkspace("esa-cli")

// chromeName はエラーメッセージ用の表示名。
const chromeName = chromecookie.ChromeName

// cookieEntry は復号済みの 1 Cookie。
type cookieEntry = chromecookie.Cookie

// keychainPassword はテストの差し替え口（seam）。production では常に chromecookie.KeychainPassword。
// 実物は security を起動して Keychain を読むため、テストからは fake に差し替える。
var keychainPassword = chromecookie.KeychainPassword

// installCleanupOnSignal は②（シグナルで終わっても一時コピーを消す）を仕掛ける。main の先頭で 1 回。
func installCleanupOnSignal() { ws.InstallCleanupOnSignal() }

// sweepStaleCookieDirs は③（前回の実行が SIGKILL 等で残した一時コピーを消す）。
func sweepStaleCookieDirs() { ws.SweepStaleTempDirs() }

// runAllCleanups は登録済みの一時コピーをすべて消す。
func runAllCleanups() { ws.RunAllCleanups() }

// extractCookies は指定プロファイル（Chrome）から全 Cookie を復号して返す。
//
// エラーは自動検出の分類（profile.go の profileSkipError）へ写し替えて返す:
//   - Keychain の取得失敗・作業領域の異常（chromecookie.EnvError）: そのまま返す = 即停止
//   - Cookie DB が無い: profileSkipError（黙って skip）
//   - 読めない・壊れた DB（chromecookie.ReadError）: profileSkipError{broken}（記録して skip）
//   - それ以外: そのまま返す（未知のものは止める側に倒す）
//
// 目的の Cookie が無かったときの原因（全件の復号失敗・-wal を読めない）は、戻り値の
// Result.Diagnose で buildClientForProfile が確かめる。
func extractCookies(profile string) (chromecookie.Result, error) {
	sweepStaleCookieDirs() // ③: 前回の実行が強制終了で残したものを先に片付ける

	password, err := keychainPassword()
	if err != nil {
		return chromecookie.Result{}, err
	}
	res, err := ws.ReadCookies(profile, password)
	if err != nil {
		return chromecookie.Result{}, classifyReadError(profile, err)
	}
	return res, nil
}

// classifyReadError は chromecookie の読み取りエラーを自動検出の分類へ写す。
func classifyReadError(profile string, err error) error {
	if chromecookie.IsMissing(err) {
		return &profileSkipError{msg: err.Error() + "\n  - プロファイル名が正しいか確認してください（-profile / ESA_CHROME_PROFILE）。"}
	}
	if re, ok := chromecookie.AsProfileIssue(profile, err); ok {
		return &profileSkipError{
			msg:    fmt.Sprintf("プロファイル %q の %v", profile, re.Err),
			broken: true,
			perm:   re.Err.Kind == chromecookie.ReadDenied,
		}
	}
	return err
}

// buildCookieHeader は対象ホスト宛ての Cookie ヘッダ文字列を組み立てる。
// 同名 Cookie が複数ある場合は、より具体的な host（長い host_key）を優先する。
func buildCookieHeader(cookies []cookieEntry, reqHost string) (string, int) {
	type pick struct {
		value string
		rank  int // 大きいほど具体的
	}
	best := map[string]pick{}
	for _, c := range cookies {
		if !chromecookie.HostMatches(c.Host, reqHost) {
			continue
		}
		rank := len(strings.TrimPrefix(c.Host, "."))
		if !strings.HasPrefix(c.Host, ".") {
			rank += 1000 // host-only を最優先
		}
		if cur, ok := best[c.Name]; !ok || rank > cur.rank {
			best[c.Name] = pick{value: c.Value, rank: rank}
		}
	}
	if len(best) == 0 {
		return "", 0
	}
	parts := make([]string, 0, len(best))
	for name, p := range best {
		parts = append(parts, name+"="+p.value)
	}
	return strings.Join(parts, "; "), len(best)
}
