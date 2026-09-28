package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 認証確認（authOK）と、プロファイルの自動検出・明示指定・setup の走査が、
// 「未ログイン」と「確認できなかった / 環境エラー」を取り違えないことを固定する。
//
// Keychain・Chrome のファイルには触らない。buildProfileClient / listScanProfiles /
// listSetupProfiles（profile.go の seam）を fake に差し替え、fake が返すクライアントは
// httptest サーバを向ける。サーバは esa の受け側の応答（404・429・5xx・ログインページ）を模す。

// statusServer は /posts への応答を status で返すサーバ。200 なら検索ページの HTML を返す。
func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body>検索ページ</body></html>"))
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadURL は接続できない URL（閉じたサーバ）を返す。
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

// ssoRedirectServer は未ログインの esa が SSO 等の別ホストへ飛ばす形を模す
// （127.0.0.1 で受けて localhost へ 302。CheckRedirect がホスト変更で止める）。
func ssoRedirectServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/sso/login", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// blockRealBackends は、テストが退行して通信・Cookie 取得へ進んだとき、開発者の実環境
// （ESA_TOKEN・config.yml の team / profile・Keychain・実 esa・api.esa.io）に届かないようにする。
// 届いた時点で t.Fatal にする。
func blockRealBackends(t *testing.T) {
	t.Helper()
	t.Setenv("ESA_TOKEN", "")
	// team / profile を固定値にする。開発者の環境（ESA_TEAM 未設定なら requireTeam で手前に止まる等）に
	// よって、退行した経路が通信側へ進むかどうかが変わらないようにする（進めば下の fake が Fatal にする）。
	t.Setenv("ESA_TEAM", "isolated-test-team")
	t.Setenv("ESA_CHROME_PROFILE", profileAuto)
	t.Setenv("HOME", t.TempDir())
	resetFileConfig(t) // XDG_CONFIG_HOME を空の一時ディレクトリへ向け、config.yml のキャッシュを捨てる
	t.Setenv("TMPDIR", t.TempDir())
	origBuild, origScan, origSetup, origKey, origAPI := buildProfileClient, listScanProfiles, listSetupProfiles, keychainPassword, esaAPIBase
	t.Cleanup(func() {
		buildProfileClient, listScanProfiles, listSetupProfiles, keychainPassword, esaAPIBase = origBuild, origScan, origSetup, origKey, origAPI
	})
	buildProfileClient = func(config, string) (*client, error) {
		t.Fatal("テストが Cookie の取得（buildProfileClient）まで進んだ")
		return nil, nil
	}
	listScanProfiles = func() []string { t.Fatal("テストがプロファイル走査まで進んだ"); return nil }
	listSetupProfiles = func() []profileInfo { t.Fatal("テストが setup の候補列挙まで進んだ"); return nil }
	keychainPassword = func() ([]byte, error) { t.Fatal("テストが Keychain まで進んだ"); return nil, nil }
	esaAPIBase = deadURL(t)
}

func testClient(baseURL string) *client {
	return &client{http: newHTTPClient(), baseURL: baseURL, cookieHeader: "session=x"}
}

// fakeProfiles は seam を差し替え、プロファイル名 → 振る舞いの表で fake を作る。
// url が空なら buildErr を返す（Cookie が無い / 環境エラーの再現）。
type fakeProfile struct {
	url      string
	buildErr error
}

type fakeEnv struct {
	built    []string // buildProfileClient が呼ばれた順
	teams    []string // そのとき渡された cfg.team
	scanned  int      // listScanProfiles が呼ばれた回数
	profiles map[string]fakeProfile
}

func installFakeProfiles(t *testing.T, order []string, profiles map[string]fakeProfile) *fakeEnv {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // プロファイルの検出キャッシュを隔離する
	env := &fakeEnv{profiles: profiles}
	origBuild, origScan, origSetup := buildProfileClient, listScanProfiles, listSetupProfiles
	t.Cleanup(func() { buildProfileClient, listScanProfiles, listSetupProfiles = origBuild, origScan, origSetup })
	buildProfileClient = func(cfg config, profile string) (*client, error) {
		env.built = append(env.built, profile)
		env.teams = append(env.teams, cfg.team)
		p, ok := env.profiles[profile]
		if !ok {
			t.Fatalf("想定外のプロファイル %q を試した", profile)
		}
		if p.url == "" {
			return nil, p.buildErr
		}
		return testClient(p.url), nil
	}
	listScanProfiles = func() []string { env.scanned++; return order }
	listSetupProfiles = func() []profileInfo {
		out := make([]profileInfo, 0, len(order))
		for _, d := range order {
			out = append(out, profileInfo{dir: d})
		}
		return out
	}
	return env
}

// redirectLoopServer は同一ホスト内でリダイレクトし続ける（上限超過 = プロファイル固有の失敗）。
func redirectLoopServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthOKDistinguishesUnauthenticatedFromFailure(t *testing.T) {
	loginPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<form action="/users/sign_in">`))
	}))
	t.Cleanup(loginPage.Close)

	const (
		authed  = "認証済み"
		unauth  = "未認証"
		profile = "プロファイル固有"
		team    = "チーム全体"
	)
	cases := []struct {
		name, url, want string
		reason          string // 未認証のとき reason に含まれるべき語
	}{
		{"200 の検索ページ", statusServer(t, 200).URL, authed, ""},
		{"404（非公開チーム）", statusServer(t, 404).URL, unauth, "404"},
		{"401", statusServer(t, 401).URL, unauth, "セッション切れ"},
		{"403", statusServer(t, 403).URL, unauth, "セッション切れ"},
		{"ログインページ", loginPage.URL, unauth, "セッション切れ"},
		{"別ホスト（SSO）へのリダイレクト", ssoRedirectServer(t).URL, unauth, "localhost"},
		{"400", statusServer(t, 400).URL, profile, ""},
		{"431", statusServer(t, 431).URL, profile, ""},
		{"同一ホストのリダイレクトループ", redirectLoopServer(t).URL, profile, ""},
		{"429", statusServer(t, 429).URL, team, ""},
		{"500", statusServer(t, 500).URL, team, ""},
		{"503", statusServer(t, 503).URL, team, ""},
		{"接続できない", deadURL(t), team, ""},
	}
	for _, c := range cases {
		ok, reason, err := testClient(c.url).authOK()
		var pe *errAuthProfileSpecific
		got := authed
		switch {
		case errors.As(err, &pe):
			got = profile
		case err != nil:
			got = team
		case !ok:
			got = unauth
		}
		if got != c.want {
			t.Errorf("%s: %s と判定した（err=%v）, want %s", c.name, got, err, c.want)
		}
		if c.reason != "" && !strings.Contains(reason, c.reason) {
			t.Errorf("%s: 理由 %q に %q が無い", c.name, reason, c.reason)
		}
	}
}

// 認証の確認がプロファイル固有の理由で失敗したら、記録して次へ進むこと。
// キャッシュ済みプロファイルで起きても走査へ進み、成功したらキャッシュを書き直すこと
// （止めると、そのプロファイルが後ろの認証済みプロファイルを永久に塞ぐ）。
func TestProfileSpecificAuthFailureIsSkipped(t *testing.T) {
	for name, url := range map[string]string{"431": statusServer(t, 431).URL, "リダイレクトループ": redirectLoopServer(t).URL} {
		t.Run("走査/"+name, func(t *testing.T) {
			installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{"A": {url: url}, "B": {url: statusServer(t, 200).URL}})
			var got string
			var err error
			captureStdio(t, func() { got, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
			if err != nil || got != "B" {
				t.Errorf("プロファイル固有の失敗で止まった: name=%q err=%v", got, err)
			}
		})
		t.Run("キャッシュ/"+name, func(t *testing.T) {
			env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{"A": {url: url}, "B": {url: statusServer(t, 200).URL}})
			writeProfileCache("t", "A")
			var got string
			var err error
			captureStdio(t, func() { got, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
			if err != nil || got != "B" || env.scanned != 1 {
				t.Errorf("キャッシュの失敗で走査へ進まない: name=%q err=%v scanned=%d", got, err, env.scanned)
			}
			if c := readProfileCache("t"); c != "B" {
				t.Errorf("成功したプロファイルでキャッシュを書き直していない: %q", c)
			}
		})
	}

	t.Run("全部失敗なら理由を添える", func(t *testing.T) {
		installFakeProfiles(t, []string{"A"}, map[string]fakeProfile{"A": {url: statusServer(t, 400).URL}})
		_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
		if err == nil || !strings.Contains(err.Error(), `"A"`) || !strings.Contains(err.Error(), "400") {
			t.Errorf("理由が添えられていない: %v", err)
		}
	})

	t.Run("setup は候補に残して「認証を確認できず」の印を付け、選べる", func(t *testing.T) {
		installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{"A": {url: statusServer(t, 400).URL}, "B": {url: statusServer(t, 200).URL}})
		dir := resetFileConfig(t)
		out, err := runSetupCapture(t, "\n1\n", "-team", "t") // [1] = A（確認できず）を選ぶ
		if err != nil {
			t.Fatalf("setup がプロファイル固有の失敗で止まった: %v", err)
		}
		if !strings.Contains(out, "認証を確認できず") || !strings.Contains(out, "400") {
			t.Errorf("候補に「認証を確認できず（理由）」の印が無い:\n%s", out)
		}
		if strings.Contains(out, "使えなかったため候補から外しました") {
			t.Errorf("プロファイル固有の失敗を候補から外している:\n%s", out)
		}
		if b, _ := os.ReadFile(dir + "/esa-cli/config.yml"); !strings.Contains(string(b), "profile: A") {
			t.Errorf("印付きの候補を選べない:\n%s", b)
		}
	})
}

// 認証を確認できない応答（429・5xx・接続失敗）で走査を中断し、そのエラーを返すこと。
// 次のプロファイルへ進んで「ログインし直して」と誤案内しないこと。
func TestAutoDetectStopsOnAuthCheckFailure(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantMsg string
	}{
		{"429", statusServer(t, 429).URL, "429"},
		{"500", statusServer(t, 500).URL, "500"},
		{"接続失敗", deadURL(t), "リクエスト失敗"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
				"A": {url: c.url},
				"B": {url: statusServer(t, 200).URL},
			})
			_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
			if err == nil {
				t.Fatal("中断すべきところで成功した（次のプロファイルへ進んだ）")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("元のエラーが伝わっていない: %v", err)
			}
			if strings.Contains(err.Error(), "ログインし直して") {
				t.Errorf("未ログインと誤案内している: %v", err)
			}
			if !strings.Contains(err.Error(), "-profile で別のプロファイル") || !strings.Contains(err.Error(), "profile-t") {
				t.Errorf("逃げ道（-profile / キャッシュのパス）が添えられていない: %v", err)
			}
			if len(env.built) != 1 {
				t.Errorf("走査を中断していない: 試したプロファイル=%v", env.built)
			}
		})
	}
}

// 未認証（404 / 401）のプロファイルは飛ばして次へ進むこと（従来の挙動の回帰）。
func TestAutoDetectSkipsUnauthenticatedProfiles(t *testing.T) {
	for st, url := range map[int]string{404: statusServer(t, 404).URL, 401: statusServer(t, 401).URL, 302: ssoRedirectServer(t).URL} {
		env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
			"A": {url: url},
			"B": {url: statusServer(t, 200).URL},
		})
		var name string
		var err error
		captureStdio(t, func() { name, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
		if err != nil || name != "B" {
			t.Errorf("status=%d: name=%q err=%v, want B", st, name, err)
		}
		if strings.Join(env.built, ",") != "A,B" {
			t.Errorf("status=%d: 試した順が違う: %v", st, env.built)
		}
	}
}

// キャッシュ済みプロファイルで認証を確認できなかったら、走査に進まずに止めること。
func TestAutoDetectCachedProfileFailureStopsBeforeScan(t *testing.T) {
	env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
		"A": {url: statusServer(t, 503).URL},
		"B": {url: statusServer(t, 200).URL},
	})
	writeProfileCache("t", "A")
	if readProfileCache("t") != "A" {
		t.Fatal("前提: キャッシュが書けていない")
	}
	_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("503 が伝わっていない: %v", err)
	}
	if env.scanned != 0 {
		t.Errorf("キャッシュの確認に失敗したのに走査へ進んだ（%d 回）", env.scanned)
	}
}

// 環境エラー（Keychain・権限）は走査を続けずに元の案内付きで返すこと。
// Cookie が無いプロファイル（profileSkipError）だけを飛ばすこと。
func TestAutoDetectEnvironmentErrorIsNotSwallowed(t *testing.T) {
	keychainErr := errors.New("Keychain から暗号化キーを取得できませんでした（テスト用）")
	env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
		"A": {buildErr: keychainErr},
		"B": {url: statusServer(t, 200).URL},
	})
	_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
	if !errors.Is(err, keychainErr) {
		t.Fatalf("環境エラーがそのまま返っていない: %v", err)
	}
	if len(env.built) != 1 {
		t.Errorf("環境エラーの後も走査を続けた: %v", env.built)
	}

	env = installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
		"A": {buildErr: &profileSkipError{msg: "Cookie がありません（テスト用）"}},
		"B": {url: statusServer(t, 200).URL},
	})
	var name string
	captureStdio(t, func() { name, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
	if err != nil || name != "B" {
		t.Errorf("Cookie の無いプロファイルを飛ばしていない: name=%q err=%v", name, err)
	}
}

// 実物の分類: Cookie DB が無いのは黙って skip、読めない（権限）のは skip + 記録（perm）。
func TestCookieErrorsClassification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := cookieDBSourcePath("NoSuchProfile"); !isProfileSkip(err) {
		t.Errorf("Cookie DB が無いのに skip 扱いにならない: %v", err)
	}

	t.Setenv("TMPDIR", t.TempDir())
	src := filepath.Join(t.TempDir(), "Cookies")
	if err := os.WriteFile(src, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(src); err == nil {
		t.Skip("権限 000 のファイルが読めてしまう環境（root 等）なので判定できない")
	}
	_, _, err := copyCookieDB("P", src)
	var ps *profileSkipError
	if !errors.As(err, &ps) || !ps.broken || !ps.perm || !strings.Contains(err.Error(), `"P"`) {
		t.Errorf("権限エラーをプロファイル名付きの skip + 記録（perm）にしていない: %v", err)
	}
}

// -profile 明示指定は自動判定の誤りに対する逃げ道なので、未認証・プロファイル固有の確認失敗では
// 止めずに理由付きの警告を stderr に出して進む。チーム全体の確認不能（429 等）はエラー。
func TestExplicitProfileWarnsButProceeds(t *testing.T) {
	for name, c := range map[string]struct{ url, reason string }{
		"404":        {statusServer(t, 404).URL, "404"},
		"SSO リダイレクト": {ssoRedirectServer(t).URL, "localhost"},
		"400":        {statusServer(t, 400).URL, "400"},
		// チーム全体の失敗でも止めない（検索エンドポイントだけの 5xx で show / meta まで止めない）。
		"429":  {statusServer(t, 429).URL, "429"},
		"503":  {statusServer(t, 503).URL, "503"},
		"接続失敗": {deadURL(t), "リクエスト失敗"},
	} {
		t.Run(name, func(t *testing.T) {
			installFakeProfiles(t, nil, map[string]fakeProfile{"P": {url: c.url}})
			var got string
			var cl *client
			var err error
			_, stderr := captureStdio(t, func() { got, cl, err = resolveProfileClient(config{team: "t", profile: "P"}) })
			if err != nil || cl == nil || got != "P" {
				t.Fatalf("明示指定を拒否した: name=%q c=%v err=%v", got, cl, err)
			}
			if !strings.Contains(stderr, "警告") || !strings.Contains(stderr, c.reason) || !strings.Contains(stderr, "ログインし直して") {
				t.Errorf("理由付きの警告が出ていない: %q", stderr)
			}
		})
	}

	// Cookie を読めないときだけエラー。
	keychainErr := errors.New("Keychain 拒否（テスト用）")
	installFakeProfiles(t, nil, map[string]fakeProfile{"P": {buildErr: keychainErr}})
	if _, c, err := resolveProfileClient(config{team: "t", profile: "P"}); !errors.Is(err, keychainErr) || c != nil {
		t.Errorf("Cookie を読めないのにエラーにしない: c=%v err=%v", c, err)
	}

	installFakeProfiles(t, nil, map[string]fakeProfile{"P": {url: statusServer(t, 200).URL}})
	var name string
	var c *client
	var err error
	_, stderr := captureStdio(t, func() { name, c, err = resolveProfileClient(config{team: "t", profile: "P"}) })
	if err != nil || c == nil || name != "P" || stderr != "" {
		t.Errorf("認証済みで失敗した / 余計な警告: name=%q c=%v err=%v stderr=%q", name, c, err, stderr)
	}
}

// setup の候補列挙も、環境エラーと認証確認の失敗を「候補なし」「未認証」に化けさせないこと。
func TestSetupStopsOnEnvironmentAndAuthCheckErrors(t *testing.T) {
	keychainErr := errors.New("Keychain から暗号化キーを取得できませんでした（テスト用）")
	cases := []struct {
		name string
		a    fakeProfile
		want string
	}{
		{"環境エラー", fakeProfile{buildErr: keychainErr}, "Keychain"},
		{"429", fakeProfile{url: statusServer(t, 429).URL}, "429"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
				"A": c.a,
				"B": {url: statusServer(t, 200).URL},
			})
			err := runSetupWithInput(t, "\n1\n", "-team", "t")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("エラーが伝わっていない: %v", err)
			}
			if len(env.built) != 1 {
				t.Errorf("中断していない: %v", env.built)
			}
		})
	}
}

// runSetupWithInput は stdin に input を流して cmdSetup を実行する（出力は捨てる）。
func runSetupWithInput(t *testing.T, input string, args ...string) error {
	t.Helper()
	_, err := runSetupCapture(t, input, args...)
	return err
}

// runSetupCapture は stdin に input を流して cmdSetup を実行し、stdout を返す。
func runSetupCapture(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; _ = r.Close() })
	var runErr error
	stdout, _ := captureStdio(t, func() { runErr = cmdSetup(args) })
	return stdout, runErr
}
