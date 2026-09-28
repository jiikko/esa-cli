package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// profileAuto は「ログイン済みプロファイルを自動検出する」ことを示す予約値。
const profileAuto = "auto"

// profileSkipError は「このプロファイルは使えないが、他のプロファイルは試してよい」ことを示す。
// 自動検出はこのエラーのときだけ次のプロファイルへ進む。
//
//   - broken=false: Cookie DB が無い / esa 宛ての Cookie が 0 件（ふつうの状態。黙って飛ばす）
//   - broken=true: このプロファイル固有の失敗（壊れた DB・sqlite 以外のファイル・古いスキーマ・
//     全件の復号失敗・Cookie DB / -wal / -shm を stat・読み取りできない）。飛ばすが理由を記録し、
//     最終的に見つからなかったときのエラー文に添える。権限（EACCES / EPERM）なら perm も立て、
//     フルディスクアクセスとパーミッションの両方を確認する案内を添える
//
// 🚨 即停止にするのは、本当にプロファイルに依存しないもの（Keychain の取得失敗・一時ディレクトリの
// 異常）と、認証を確認できない応答（ネットワーク・429・5xx）だけ。走査を続けずに元の案内付きで返す。
// 以前は全部 continue で捨てており、Keychain の拒否が「Cookie を持つプロファイルが見つかりません」に
// 化けたうえ、プロファイル数だけ security を起動していた。
// 権限エラーを即停止にしないこと: chmod 000 や root 所有（sudo で起動した Chrome が作ったもの）の
// プロファイルはプロファイル固有で、フルディスクアクセスを付けても直らない（止めると後ろの正常な
// プロファイルが使えなくなる。変更前の continue はそれを使えていた）。
// 分類は許可リスト（cookies.go / ここで作るものだけ skip）。未知のエラーは止める側に倒す。
type profileSkipError struct {
	msg    string
	broken bool
	perm   bool // broken のうち権限（EACCES / EPERM）で読めなかったもの
}

func (e *profileSkipError) Error() string { return e.msg }

func isProfileSkip(err error) bool {
	var ps *profileSkipError
	return errors.As(err, &ps)
}

// skippedProfiles は「壊れている / 読めない / 認証をプロファイル固有の理由で確認できない」ため飛ばしたプロファイルの理由を集める。
type skippedProfiles struct {
	msgs []string
	perm bool // 権限で読めなかったものが 1 つでもあるか
}

// note は err が broken な skip なら理由を記録する。
func (s *skippedProfiles) note(err error) {
	var ps *profileSkipError
	if errors.As(err, &ps) && ps.broken {
		s.msgs = append(s.msgs, ps.msg)
		s.perm = s.perm || ps.perm
	}
}

// suffix はエラー文に添える記述を返す（何も無ければ空）。
func (s skippedProfiles) suffix() string {
	if len(s.msgs) == 0 {
		return ""
	}
	out := "\n  使えなかったため飛ばしたプロファイル:\n    - " + strings.Join(s.msgs, "\n    - ")
	if s.perm {
		out += permissionHint
	}
	return out
}

// permissionHint は Cookie DB のアクセス拒否の案内。原因は環境（フルディスクアクセス）と
// プロファイル固有（パーミッション・所有者）のどちらもありうるので、両方を挙げる。
const permissionHint = "\n  アクセス拒否で読めないプロファイルがあります。次の両方を確認してください:\n" +
	"    - ターミナル（またはこのツールを起動しているアプリ）に「フルディスクアクセス」があるか\n" +
	"      （システム設定 → プライバシーとセキュリティ → フルディスクアクセス）\n" +
	"    - プロファイルのディレクトリ / Cookie ファイルの所有者とパーミッション\n" +
	"      （sudo で起動した Chrome が作ると root 所有になる。ls -l で確認）"

// buildClientForProfile は指定プロファイルの Cookie でクライアントを構築する。
// esa 宛て Cookie が無い場合は *profileSkipError（自動検出時は次の候補へ進むために使う）。
func buildClientForProfile(cfg config, profile string) (*client, error) {
	cookies, err := extractCookiesForProfile(profile)
	if err != nil {
		return nil, err
	}
	header, n := buildCookieHeader(cookies, cfg.teamHost())
	if n == 0 {
		return nil, &profileSkipError{msg: fmt.Sprintf("%s 宛ての Cookie がプロファイル %q にありません", cfg.teamHost(), profile)}
	}
	return newClient(cfg.teamHost(), header), nil
}

// extractCookiesForProfile は extractCookies の薄いラッパー（意図を明示するため）。
func extractCookiesForProfile(profile string) ([]cookieEntry, error) {
	return extractCookies(profile)
}

// テストの差し替え口（seam）。production では常に実体を指す。
// 実体は Keychain・Chrome のファイルを読むため、テストからは fake に差し替える。
var (
	buildProfileClient = buildClientForProfile
	listScanProfiles   = listChromeProfiles
	listSetupProfiles  = listProfileInfos
)

// probeResult は 1 プロファイルを試した結果。
type probeResult struct {
	c      *client
	authed bool
	reason string // authed=false のとき、未認証 / 確認できなかった理由（警告・表示用）
}

// probeProfile は 1 プロファイルを試し、クライアントと「認証が通るか」を返す。
//
//   - err が *profileSkipError: このプロファイルは使えない（次を試してよい）。
//     Cookie が無い / 壊れている / 読めない（res.c == nil）か、認証の確認がプロファイル固有の
//     理由で失敗した（res.c != nil。-profile 明示指定ではこれを警告にして先へ進む）
//   - err がそれ以外: 環境エラー（res.c == nil）、またはチーム全体の失敗で認証を確認できなかった
//     （ネットワーク・タイムアウト・429・5xx。res.c != nil）。どのプロファイルでも同じなので、
//     自動検出は走査を止めてこのエラーを返す
//   - err == nil: res.authed が認証の可否
func probeProfile(cfg config, profile string) (probeResult, error) {
	c, err := buildProfileClient(cfg, profile)
	if err != nil {
		return probeResult{}, err
	}
	authed, reason, err := c.authOK()
	var pe *errAuthProfileSpecific
	if errors.As(err, &pe) {
		msg := fmt.Sprintf("プロファイル %q で %s の認証を確認できません: %v", profile, cfg.teamHost(), pe.err)
		return probeResult{c: c, reason: pe.err.Error()}, &profileSkipError{msg: msg, broken: true}
	}
	if err != nil {
		// チーム全体の失敗でも c は返す（-profile 明示指定は警告して本処理へ進むため）。
		return probeResult{c: c, reason: err.Error()}, fmt.Errorf("プロファイル %q で %s の認証状態を確認できませんでした（未ログインとは判定していません）: %w",
			profile, cfg.teamHost(), err)
	}
	return probeResult{c: c, authed: authed, reason: reason}, nil
}

// withTeamWideHint は、自動検出がチーム全体の失敗（5xx 等）で止まるときのエラーに逃げ道を添える。
// 「チーム全体」は応答コードからの推定で、特定のプロファイルの Cookie が原因のこともある
// （そのプロファイルがキャッシュされていると毎回そこで止まる）。環境エラー（res.c == nil）には添えない。
func withTeamWideHint(cfg config, res probeResult, err error) error {
	if res.c == nil {
		return err
	}
	cache, perr := profileCachePath(cfg.team)
	if perr != nil {
		cache = "~/.config/esa-cli/cache/profile-" + cfg.team
	}
	return fmt.Errorf("%w\n  特定のプロファイルの Cookie が原因の可能性があるなら、-profile で別のプロファイルを指定するか、"+
		"キャッシュ（%s）を削除して再実行してください。", err, cache)
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
		// 明示指定でも認証を 1 回確かめる。確かめないと、セッション切れが
		// 後続の本処理で「見つかりません（404）」に見える（非公開チームの挙動）。
		// 🚨 認証を確認できなくても（未認証・プロファイル固有・チーム全体の失敗のどれでも）拒否はせず、
		// 警告して本処理へ進む。明示指定は自動判定（authOK）が誤ったときの逃げ道なので塞がない。
		// 検索エンドポイント（/posts?q=）だけが 5xx でも show / meta は通ることがある。本処理が
		// 同じ失敗なら本処理のエラーで止まる。止めるのは Cookie を読めないとき（res.c == nil）だけ。
		res, err := probeProfile(cfg, cfg.profile)
		if res.c == nil {
			var ps *profileSkipError
			if errors.As(err, &ps) && ps.perm {
				return cfg.profile, nil, fmt.Errorf("%w%s", err, permissionHint)
			}
			return cfg.profile, nil, err
		}
		if !res.authed {
			fmt.Fprintf(os.Stderr,
				"esa: 警告: プロファイル %q では %s の認証を確認できませんでした（理由: %s）。"+
					"セッション切れなら %s のこのプロファイルで https://%s にログインし直してください。\n",
				cfg.profile, cfg.teamHost(), res.reason, chromeName, cfg.teamHost())
		}
		return cfg.profile, res.c, nil
	}

	var skipped skippedProfiles

	// 1. キャッシュ済みプロファイルを試す（前回の自動検出結果）。
	cached := readProfileCache(cfg.team)
	if cached != "" {
		res, err := probeProfile(cfg, cached)
		switch {
		case err == nil && res.authed:
			return cached, res.c, nil
		case err != nil && !isProfileSkip(err):
			return "", nil, withTeamWideHint(cfg, res, err) // 環境エラー / チーム全体の確認不能。走査しても同じなので止める
		}
		// Cookie が無くなった / 壊れた / 未ログインになった / プロファイル固有の確認失敗: 走査へ進む
		// （走査で同じプロファイルをもう一度試すので、理由はそこで記録する。成功したらキャッシュを書き直す）
	}

	// 2. 全プロファイルを走査し、認証が通る最初のものを採用する。
	profiles := listScanProfiles()
	var triedWithCookie int
	for _, p := range profiles {
		res, err := probeProfile(cfg, p)
		if isProfileSkip(err) {
			skipped.note(err) // 壊れたプロファイルは理由を残して飛ばす
			continue          // esa Cookie が無い / DB が無いプロファイルは黙って飛ばす
		}
		if err != nil {
			return "", nil, withTeamWideHint(cfg, res, err)
		}
		triedWithCookie++
		if res.authed {
			writeProfileCache(cfg.team, p)
			fmt.Fprintf(os.Stderr, "esa: ログイン済みプロファイル %q を自動検出しました（esa config set profile %q で固定できます）\n", p, p)
			return p, res.c, nil
		}
	}

	if triedWithCookie > 0 {
		return "", nil, fmt.Errorf(
			"esa の Cookie を持つプロファイルはありましたが、いずれも認証が通りませんでした（%d 件試行）。\n"+
				"  %s の Web にログインしている %s プロファイルか確認し、セッションが切れていればログインし直してください。%s",
			triedWithCookie, cfg.teamHost(), chromeName, skipped.suffix())
	}
	return "", nil, fmt.Errorf(
		"%s 宛ての Cookie を持つ %s プロファイルが見つかりませんでした。\n"+
			"  %s で https://%s にログインしているか確認してください（このツールは Chrome 専用です）。%s",
		cfg.teamHost(), chromeName, chromeName, cfg.teamHost(), skipped.suffix())
}

// listChromeProfiles は Local State からプロファイルのディレクトリ名を列挙する。
// 読み取れない場合は既定的な候補（Default / Profile N）にフォールバックする。
func listChromeProfiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{"Default"}
	}
	lsPath := filepath.Join(home, "Library", "Application Support", chromeSupportSubdir, "Local State")
	data, err := os.ReadFile(lsPath)
	if err != nil {
		return fallbackProfiles(home)
	}
	// info_cache のキー（プロファイルのディレクトリ名）だけを取り出す。暗号鍵等は読まない。
	var ls struct {
		Profile struct {
			InfoCache map[string]json.RawMessage `json:"info_cache"`
			LastUsed  string                     `json:"last_used"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(data, &ls); err != nil || len(ls.Profile.InfoCache) == 0 {
		return fallbackProfiles(home)
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
func fallbackProfiles(home string) []string {
	base := filepath.Join(home, "Library", "Application Support", chromeSupportSubdir)
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

// errAuthProfileSpecific は、認証の確認がこのプロファイル固有の理由で失敗したことを示す
// （401/403/404 以外の 4xx・同一ホスト内のリダイレクトループ・その他の非 200）。
// Cookie の中身（巨大な Cookie で 431 / 壊れた値で 400 等）で起きうるので、他のプロファイルは試してよい。
type errAuthProfileSpecific struct{ err error }

func (e *errAuthProfileSpecific) Error() string { return e.err.Error() }
func (e *errAuthProfileSpecific) Unwrap() error { return e.err }

// authOK は「ログイン済みか」を軽量に判定する。
// 非公開チームでは未認証だと全パスが 404 になるため、検索ページが 200 で
// 返るか（かつログインページでないか）で判定する。reason は未認証と判定した理由。
//
// 🚨 失敗は 3 つに分ける:
//   - 未認証（false, reason, nil）: セッション切れ / 401 / 403 / 404 / 別ホスト（SSO 等）へのリダイレクト
//   - プロファイル固有（*errAuthProfileSpecific）: 上記以外の 4xx・リダイレクトループ・その他の非 200。
//     走査は記録して次のプロファイルへ進む
//   - チーム全体（それ以外の error）: ネットワーク断・タイムアウト・429・5xx。どのプロファイルでも
//     同じ結果なので走査を止める。これを false にすると全プロファイルを走査して「ログインし直して」と
//     誤案内し、プロファイル固有の失敗を止める側にすると 1 つの壊れたプロファイル（キャッシュ済み等）が
//     後ろの認証済みプロファイルを永久に塞ぐ
func (c *client) authOK() (authed bool, reason string, err error) {
	b, ct, err := c.get("/posts?q=esa")
	if err != nil {
		var se *errSessionExpired
		var nf *errNotFound
		var rb *errRedirectBlocked
		var hs *errHTTPStatus
		var tr *errTooManyRedirects
		switch {
		case errors.As(err, &se):
			return false, "セッション切れ（ログインページ / 401 / 403 が返った）", nil
		case errors.As(err, &nf):
			return false, "404（非公開チームでは未ログイン時の応答）", nil
		case errors.As(err, &rb):
			// 資格情報は送らずに止めてある。リダイレクト先ホストは rb の文言に入っている。
			return false, rb.Error(), nil
		case errors.As(err, &tr):
			return false, "", &errAuthProfileSpecific{err}
		case errors.As(err, &hs) && hs.code != http.StatusTooManyRequests && hs.code < 500:
			return false, "", &errAuthProfileSpecific{err}
		}
		return false, "", err
	}
	if !strings.Contains(ct, "html") || looksLikeLoginPage(b, ct) {
		return false, fmt.Sprintf("検索ページ以外が返った（Content-Type=%q）", ct), nil
	}
	return true, "", nil
}

// --- 検出結果のキャッシュ（cwd 非依存: UserConfigDir 配下） ---

func profileCachePath(team string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	// チームごとに分ける。
	// 🚨 以前は "profile-<ブラウザ>-<team>" だった（issue 003 で Chrome 専用にした際に
	// ブラウザ成分を落とした）。旧名のファイルは孤児として残るが、自動検出が一度だけ
	// 余計に走って新しい名前で書き直されるだけなので、掃除機構は持たない。
	return filepath.Join(cacheDir, fmt.Sprintf("profile-%s", team)), nil
}

func readProfileCache(team string) string {
	p, err := profileCachePath(team)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeProfileCache(team, profile string) {
	p, err := profileCachePath(team)
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
func listProfileInfos() []profileInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	lsPath := filepath.Join(home, "Library", "Application Support", chromeSupportSubdir, "Local State")
	data, err := os.ReadFile(lsPath)
	if err != nil {
		// フォールバック: ディレクトリ名のみ
		var out []profileInfo
		for _, d := range fallbackProfiles(home) {
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
		for _, d := range fallbackProfiles(home) {
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
