package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// profileAuto は「ログイン済みプロファイルを自動検出する」ことを示す予約値。
const profileAuto = "auto"

// buildClientForProfile は指定プロファイルの Cookie でクライアントを構築する。
// esa 宛て Cookie が無い場合はエラー（自動検出時は次の候補へ進むために使う）。
func buildClientForProfile(cfg config, profile string) (*client, error) {
	cookies, err := extractCookiesForProfile(cfg.browser, profile)
	if err != nil {
		return nil, err
	}
	header, n := buildCookieHeader(cookies, cfg.teamHost())
	if n == 0 {
		return nil, fmt.Errorf("%s 宛ての Cookie がプロファイル %q にありません", cfg.teamHost(), profile)
	}
	return newClient(cfg.teamHost(), header), nil
}

// extractCookiesForProfile は extractCookies の薄いラッパー（意図を明示するため）。
func extractCookiesForProfile(browser, profile string) ([]cookieEntry, error) {
	return extractCookies(browser, profile)
}

// resolveProfile は実効プロファイルとクライアントを決定する。
// profile が "auto"（既定）のときは、ログイン済みの esa セッションを持つ
// プロファイルを自動検出する。明示指定時はそれをそのまま使う。
func resolveProfile(cfg config) (*client, error) {
	_, c, err := resolveProfileClient(cfg)
	return c, err
}

// resolveProfileClient は実効プロファイル名とクライアントの両方を返す。
// config init など「どのプロファイルが使われるか」を知りたい箇所で使う。
func resolveProfileClient(cfg config) (string, *client, error) {
	if err := cfg.requireTeam(); err != nil {
		return "", nil, err
	}
	if cfg.profile != "" && cfg.profile != profileAuto {
		c, err := buildClientForProfile(cfg, cfg.profile)
		return cfg.profile, c, err
	}

	bp, ok := browserProfiles[cfg.browser]
	if !ok {
		return "", nil, fmt.Errorf("未対応のブラウザ %q", cfg.browser)
	}

	// 1. キャッシュ済みプロファイルを試す（前回の自動検出結果）。
	if cached := readProfileCache(cfg.browser, cfg.team); cached != "" {
		if c, err := buildClientForProfile(cfg, cached); err == nil && c.authOK() {
			return cached, c, nil
		}
	}

	// 2. 全プロファイルを走査し、認証が通る最初のものを採用する。
	profiles := listBrowserProfiles(bp)
	var triedWithCookie int
	for _, p := range profiles {
		c, err := buildClientForProfile(cfg, p)
		if err != nil {
			continue // esa Cookie が無い / DB が無いプロファイルは飛ばす
		}
		triedWithCookie++
		if c.authOK() {
			writeProfileCache(cfg.browser, cfg.team, p)
			fmt.Fprintf(os.Stderr, "esa: ログイン済みプロファイル %q を自動検出しました（esa config set profile %q で固定できます）\n", p, p)
			return p, c, nil
		}
	}

	if triedWithCookie > 0 {
		return "", nil, fmt.Errorf(
			"esa の Cookie を持つプロファイルはありましたが、いずれも認証が通りませんでした（%d 件試行）。\n"+
				"  %s の Web にログインしているブラウザ/プロファイルか確認し、セッションが切れていれば Chrome でログインし直してください。",
			triedWithCookie, cfg.teamHost())
	}
	return "", nil, fmt.Errorf(
		"%s 宛ての Cookie を持つ %s プロファイルが見つかりませんでした。\n"+
			"  対象ブラウザ（-browser / ESA_BROWSER）で https://%s にログインしているか確認してください。",
		cfg.teamHost(), cfg.browser, cfg.teamHost())
}

// listBrowserProfiles は Local State からプロファイルのディレクトリ名を列挙する。
// 読み取れない場合は既定的な候補（Default / Profile N）にフォールバックする。
func listBrowserProfiles(bp browserProfile) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{"Default"}
	}
	lsPath := filepath.Join(home, "Library", "Application Support", bp.supportSubdir, "Local State")
	data, err := os.ReadFile(lsPath)
	if err != nil {
		return fallbackProfiles(home, bp)
	}
	// info_cache のキー（プロファイルのディレクトリ名）だけを取り出す。暗号鍵等は読まない。
	var ls struct {
		Profile struct {
			InfoCache map[string]json.RawMessage `json:"info_cache"`
			LastUsed  string                     `json:"last_used"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(data, &ls); err != nil || len(ls.Profile.InfoCache) == 0 {
		return fallbackProfiles(home, bp)
	}
	profiles := make([]string, 0, len(ls.Profile.InfoCache))
	for dir := range ls.Profile.InfoCache {
		profiles = append(profiles, dir)
	}
	sort.Strings(profiles)
	// 直近に使われたプロファイルを先頭へ寄せる（検出を速くする）。
	if lu := ls.Profile.LastUsed; lu != "" {
		for i, p := range profiles {
			if p == lu {
				profiles = append([]string{p}, append(profiles[:i:i], profiles[i+1:]...)...)
				break
			}
		}
	}
	return profiles
}

// fallbackProfiles は Local State が読めないときに、実在するディレクトリを走査する。
func fallbackProfiles(home string, bp browserProfile) []string {
	base := filepath.Join(home, "Library", "Application Support", bp.supportSubdir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return []string{"Default"}
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "Default" || strings.HasPrefix(name, "Profile ") {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return []string{"Default"}
	}
	sort.Strings(out)
	return out
}

// authOK は「ログイン済みか」を軽量に判定する。
// 非公開チームでは未認証だと全パスが 404 になるため、検索ページが 200 で
// 返るか（かつログインページでないか）で判定する。
func (c *client) authOK() bool {
	b, ct, err := c.get("/posts?q=esa")
	if err != nil {
		return false
	}
	return strings.Contains(ct, "html") && !looksLikeLoginPage(b, ct)
}

// --- 検出結果のキャッシュ（cwd 非依存: UserConfigDir 配下） ---

func profileCachePath(browser, team string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	// ブラウザ・チームごとに分ける。
	return filepath.Join(cacheDir, fmt.Sprintf("profile-%s-%s", browser, team)), nil
}

func readProfileCache(browser, team string) string {
	p, err := profileCachePath(browser, team)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeProfileCache(browser, team, profile string) {
	p, err := profileCachePath(browser, team)
	if err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(profile+"\n"), 0o600)
}

// profileInfo はプロファイルのディレクトリ名とログイン中アカウント情報。
type profileInfo struct {
	dir   string
	name  string // 表示名（Local State の name）
	email string // ログイン中 Google アカウント（user_name。esa ログインとは限らない点に注意）
}

// listProfileInfos は Local State からプロファイルとメール/表示名を取得する。
// 読めない場合はディレクトリ名のみ（メール空）で返す。
func listProfileInfos(bp browserProfile) []profileInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	lsPath := filepath.Join(home, "Library", "Application Support", bp.supportSubdir, "Local State")
	data, err := os.ReadFile(lsPath)
	if err != nil {
		// フォールバック: ディレクトリ名のみ
		var out []profileInfo
		for _, d := range fallbackProfiles(home, bp) {
			out = append(out, profileInfo{dir: d})
		}
		return out
	}
	var ls struct {
		Profile struct {
			InfoCache map[string]struct {
				Name     string `json:"name"`
				UserName string `json:"user_name"`
				GaiaName string `json:"gaia_name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(data, &ls); err != nil || len(ls.Profile.InfoCache) == 0 {
		var out []profileInfo
		for _, d := range fallbackProfiles(home, bp) {
			out = append(out, profileInfo{dir: d})
		}
		return out
	}
	out := make([]profileInfo, 0, len(ls.Profile.InfoCache))
	for dir, info := range ls.Profile.InfoCache {
		email := info.UserName
		if email == "" {
			email = info.GaiaName
		}
		out = append(out, profileInfo{dir: dir, name: info.Name, email: email})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out
}
