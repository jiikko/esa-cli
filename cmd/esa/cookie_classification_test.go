package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jiikko/dotfiles/src/chromecookie/chromecookietest"
)

// プロファイルの失敗の分類（即停止 / 黙って skip / skip + 理由の記録）を、
// **実物の** extractCookies → buildClientForProfile → probeProfile を通して固定する。
//
// 差し替えるのは Keychain（keychainPassword の seam）だけ。Cookie DB はテストの中で
// sqlite を作り、HOME を一時ディレクトリへ向けて Chrome の配置を再現する。
// 実 Keychain・実 Chrome・実 esa には届かない（esa 宛ての Cookie が揃ったケースでも
// クライアントを作るだけで通信はしない）。

const fakeKeychainPassword = "testpassword"

// fakeChrome は HOME / TMPDIR を隔離し、Keychain を fake にする。keychainErr が非 nil なら取得失敗を模す。
func fakeChrome(t *testing.T, keychainErr error) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	orig := keychainPassword
	t.Cleanup(func() { keychainPassword = orig })
	keychainPassword = func() ([]byte, error) {
		if keychainErr != nil {
			return nil, keychainErr
		}
		return []byte(fakeKeychainPassword), nil
	}
	return home
}

func profileDir(home, profile string) string { return chromecookietest.ProfileDir(home, profile) }

type fakeCookie = chromecookietest.Cookie

// writeCookieDB は Chrome と同じ列を持つ Cookie DB を profile の Network/Cookies に作る。
func writeCookieDB(t *testing.T, home, profile string, cookies []fakeCookie) string {
	t.Helper()
	return chromecookietest.WriteCookieDB(t, home, profile, "23", cookies)
}

// encWithPassword は password（Keychain の値に相当）で plain を暗号化する（meta.version<24 の形）。
func encWithPassword(t *testing.T, password, plain string) []byte {
	t.Helper()
	return chromecookietest.Encrypt(t, password, plain, "")
}

func permSkip(err error) bool {
	var ps *profileSkipError
	return errors.As(err, &ps) && ps.broken && ps.perm
}

func brokenSkip(err error) bool {
	var ps *profileSkipError
	return errors.As(err, &ps) && ps.broken
}

func TestProfileFailureClassification(t *testing.T) {
	cfg := config{team: "t"}

	t.Run("esa 宛ての Cookie が 0 件は黙って skip", func(t *testing.T) {
		home := fakeChrome(t, nil)
		writeCookieDB(t, home, "P", []fakeCookie{{Host: ".example.com", Name: "s", Value: "v"}})
		_, err := buildClientForProfile(cfg, "P")
		if !isProfileSkip(err) || brokenSkip(err) {
			t.Errorf("Cookie 0 件が「黙って skip」にならない: %T %v", err, err)
		}
	})

	t.Run("Cookie DB が無いのは黙って skip", func(t *testing.T) {
		fakeChrome(t, nil)
		_, err := buildClientForProfile(cfg, "NoSuch")
		if !isProfileSkip(err) || brokenSkip(err) {
			t.Errorf("DB 無しが「黙って skip」にならない: %v", err)
		}
	})

	t.Run("Keychain の取得失敗は即停止", func(t *testing.T) {
		kerr := errors.New("Keychain 拒否（テスト用）")
		home := fakeChrome(t, kerr)
		writeCookieDB(t, home, "P", nil)
		_, err := buildClientForProfile(cfg, "P")
		if isProfileSkip(err) || !errors.Is(err, kerr) {
			t.Errorf("Keychain の失敗を即停止にしていない: %v", err)
		}
	})

	t.Run("sqlite 以外のファイルは skip + 記録", func(t *testing.T) {
		home := fakeChrome(t, nil)
		dir := filepath.Join(profileDir(home, "P"), "Network")
		_ = os.MkdirAll(dir, 0o700)
		if err := os.WriteFile(filepath.Join(dir, "Cookies"), []byte(strings.Repeat("not a database ", 100)), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := buildClientForProfile(cfg, "P")
		if !brokenSkip(err) || !strings.Contains(err.Error(), `"P"`) {
			t.Errorf("壊れた DB が skip + 記録にならない: %T %v", err, err)
		}
	})

	t.Run("古いスキーマは skip + 記録", func(t *testing.T) {
		home := fakeChrome(t, nil)
		dir := filepath.Join(profileDir(home, "P"), "Network")
		_ = os.MkdirAll(dir, 0o700)
		db, err := sql.Open("sqlite", filepath.Join(dir, "Cookies"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT)`); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		_, err = buildClientForProfile(cfg, "P")
		if !brokenSkip(err) {
			t.Errorf("古いスキーマが skip + 記録にならない: %T %v", err, err)
		}
	})

	t.Run("復号に全件失敗は skip + 記録（Cookie 0 件に化けない）", func(t *testing.T) {
		home := fakeChrome(t, nil)
		writeCookieDB(t, home, "P", []fakeCookie{
			{Host: ".esa.io", Name: "_session", Enc: encWithPassword(t, "wrongpassword", "secret-a")},
			{Host: ".esa.io", Name: "other", Enc: encWithPassword(t, "wrongpassword", "secret-b")},
		})
		_, err := buildClientForProfile(cfg, "P")
		if !brokenSkip(err) || !strings.Contains(err.Error(), "復号") {
			t.Errorf("全件の復号失敗が skip + 記録にならない: %T %v", err, err)
		}
	})

	t.Run("v24: 鍵違いの長い値が多数件でも全件復号失敗として skip + 記録", func(t *testing.T) {
		home := fakeChrome(t, nil)
		var cs []fakeCookie
		for i := range 2000 {
			cs = append(cs, fakeCookie{Host: ".esa.io", Name: "c" + strconv.Itoa(i),
				Enc: chromecookietest.Encrypt(t, "wrongpassword", strings.Repeat("v", 300)+strconv.Itoa(i), ".esa.io")})
		}
		chromecookietest.WriteCookieDB(t, home, "P", "24", cs)
		_, err := buildClientForProfile(cfg, "P")
		if !brokenSkip(err) || !strings.Contains(err.Error(), "復号") {
			t.Errorf("v24 の鍵違い多数件が全件復号失敗にならない: %T %v", err, err)
		}
	})

	t.Run("v24: 正しい鍵・正規のホストハッシュなら通る", func(t *testing.T) {
		home := fakeChrome(t, nil)
		chromecookietest.WriteCookieDB(t, home, "P", "24", []fakeCookie{
			{Host: ".esa.io", Name: "_session", Enc: chromecookietest.Encrypt(t, fakeKeychainPassword, strings.Repeat("s", 300), ".esa.io")},
		})
		c, err := buildClientForProfile(cfg, "P")
		if err != nil || !strings.Contains(c.cookieHeader, "_session="+strings.Repeat("s", 300)) {
			t.Errorf("正規の v24 Cookie を復号できない: err=%v", err)
		}
	})

	t.Run("1 件だけの復号失敗は続行する", func(t *testing.T) {
		home := fakeChrome(t, nil)
		writeCookieDB(t, home, "P", []fakeCookie{
			{Host: ".esa.io", Name: "bad", Enc: encWithPassword(t, "wrongpassword", "x")},
			{Host: ".esa.io", Name: "_session", Enc: encWithPassword(t, fakeKeychainPassword, "good")},
		})
		c, err := buildClientForProfile(cfg, "P")
		if err != nil || !strings.Contains(c.cookieHeader, "_session=good") {
			t.Errorf("1 件の失敗で止めた / 正しい Cookie が入っていない: err=%v c=%+v", err, c)
		}
	})

	t.Run("Cookie DB の stat が権限エラーなら skip + 記録（perm）", func(t *testing.T) {
		home := fakeChrome(t, nil)
		writeCookieDB(t, home, "P", nil)
		pd := profileDir(home, "P")
		if err := os.Chmod(pd, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(pd, 0o700) })
		if _, err := os.Stat(filepath.Join(pd, "Network", "Cookies")); err == nil {
			t.Skip("権限 000 のディレクトリが読めてしまう環境（root 等）なので判定できない")
		}
		_, err := buildClientForProfile(cfg, "P")
		if !permSkip(err) || !strings.Contains(err.Error(), `"P"`) {
			t.Errorf("stat の権限エラーをプロファイル名付きの skip + 記録にしていない: %T %v", err, err)
		}
	})

	// 🚨 -wal / -shm の読み取り失敗は黙って捨てない。ただし本体の DB に esa 宛ての Cookie があれば
	// それで続行する（認証は authOK が確かめる）。無かったときだけ理由を記録して skip する
	// （chromecookie の Result.Diagnose。slack-cli / newrelic-nrql-cli と同じ扱い）。
	t.Run("-wal が読めず esa 宛ての Cookie も無いなら skip + 記録（perm）", func(t *testing.T) {
		home := fakeChrome(t, nil)
		path := writeCookieDB(t, home, "P", []fakeCookie{{Host: ".example.com", Name: "s", Value: "v"}})
		if err := os.WriteFile(path+"-wal", []byte("wal"), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(path + "-wal"); err == nil {
			t.Skip("権限 000 のファイルが読めてしまう環境（root 等）なので判定できない")
		}
		_, err := buildClientForProfile(cfg, "P")
		if !brokenSkip(err) || !strings.Contains(err.Error(), "Cookies-wal") {
			t.Errorf("-wal を読めなかったことを skip + 記録にしていない（「Cookie が無い」に化けた）: %T %v", err, err)
		}
	})

	t.Run("-wal が読めなくても本体に esa 宛ての Cookie があれば続行", func(t *testing.T) {
		home := fakeChrome(t, nil)
		path := writeCookieDB(t, home, "P", []fakeCookie{{Host: ".esa.io", Name: "_session", Value: "v"}})
		if err := os.Mkdir(path+"-wal", 0o700); err != nil { // ReadFile はディレクトリで EISDIR 系のエラー
			t.Fatal(err)
		}
		c, err := buildClientForProfile(cfg, "P")
		if err != nil || !strings.Contains(c.cookieHeader, "_session=v") {
			t.Errorf("取れた Cookie があるのに止めた: err=%v", err)
		}
	})

	t.Run("-wal / -shm が無いのは従来どおり続行", func(t *testing.T) {
		home := fakeChrome(t, nil)
		writeCookieDB(t, home, "P", []fakeCookie{{Host: ".esa.io", Name: "_session", Value: "v"}})
		if _, err := buildClientForProfile(cfg, "P"); err != nil {
			t.Errorf("-wal / -shm 無しで失敗した: %v", err)
		}
	})
}

// 実物の分類を通した走査: 壊れたプロファイルがあっても止まらず、最後まで見つからなければ
// 理由（プロファイル名付き）をエラーに添える。黙って skip するもの（DB 無し）は添えない。
func TestAutoDetectRecordsBrokenProfilesThroughRealBuild(t *testing.T) {
	home := fakeChrome(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(profileDir(home, "Broken"), "Network")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "Cookies"), []byte(strings.Repeat("garbage ", 200)), 0o600)
	writeCookieDB(t, home, "Undecryptable", []fakeCookie{{Host: ".esa.io", Name: "_session", Enc: encWithPassword(t, "wrongpassword", "x")}})

	origScan := listScanProfiles
	t.Cleanup(func() { listScanProfiles = origScan })
	var scanned int
	listScanProfiles = func() []string { scanned++; return []string{"Broken", "NoDB", "Undecryptable"} }
	writeProfileCache("t", "Broken") // キャッシュ済みプロファイルが壊れていても走査へ進むこと

	_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
	if err == nil {
		t.Fatal("見つからないのに成功した")
	}
	msg := err.Error()
	if scanned != 1 {
		t.Errorf("キャッシュが壊れていたのに走査へ進んでいない（%d 回）", scanned)
	}
	if !strings.Contains(msg, `"Broken"`) || !strings.Contains(msg, `"Undecryptable"`) {
		t.Errorf("壊れたプロファイルの理由が添えられていない: %v", err)
	}
	if strings.Contains(msg, `"NoDB"`) {
		t.Errorf("DB 無しのプロファイルまで理由に並べている: %v", err)
	}
	if strings.Count(msg, `"Broken"`) != 1 {
		t.Errorf("キャッシュ試行と走査で同じ理由を二重に記録している: %v", err)
	}
}

// 壊れたプロファイルの後ろの正常なプロファイルを採用すること（変更前の continue の挙動の回帰）。
// キャッシュ経路・走査・setup の 3 経路。
func TestBrokenProfileDoesNotBlockLaterProfiles(t *testing.T) {
	broken := &profileSkipError{msg: `プロファイル "A" の Cookie DB を読めません（テスト用）`, broken: true}

	t.Run("走査", func(t *testing.T) {
		installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
			"A": {buildErr: broken},
			"B": {url: statusServer(t, 200).URL},
		})
		var name string
		var err error
		captureStdio(t, func() { name, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
		if err != nil || name != "B" {
			t.Errorf("壊れたプロファイルで止まった: name=%q err=%v", name, err)
		}
	})

	t.Run("キャッシュ経路", func(t *testing.T) {
		env := installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
			"A": {buildErr: broken},
			"B": {url: statusServer(t, 200).URL},
		})
		writeProfileCache("t", "A")
		var name string
		var err error
		captureStdio(t, func() { name, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
		if err != nil || name != "B" || env.scanned != 1 {
			t.Errorf("壊れたキャッシュで走査へ進まない: name=%q err=%v scanned=%d", name, err, env.scanned)
		}
	})

	t.Run("setup", func(t *testing.T) {
		installFakeProfiles(t, []string{"A", "B"}, map[string]fakeProfile{
			"A": {buildErr: broken},
			"B": {url: statusServer(t, 200).URL},
		})
		out, err := runSetupCapture(t, "\n1\n", "-team", "t")
		if err != nil {
			t.Errorf("setup が壊れたプロファイルで止まった: %v", err)
		}
		if !strings.Contains(out, "注意: 次のプロファイルは使えなかったため候補から外しました") || !strings.Contains(out, broken.msg) {
			t.Errorf("setup の注意に壊れたプロファイルの理由が出ていない:\n%s", out)
		}
	})

	t.Run("setup で候補が無ければ理由を添える", func(t *testing.T) {
		installFakeProfiles(t, []string{"A"}, map[string]fakeProfile{"A": {buildErr: broken}})
		err := runSetupWithInput(t, "\n", "-team", "t")
		if err == nil || !strings.Contains(err.Error(), broken.msg) {
			t.Errorf("setup の「候補なし」に壊れたプロファイルの理由が無い: %v", err)
		}
	})
}

// setup / config init が保存する team は正規化（小文字化）した値であること。
func TestSavedTeamIsNormalized(t *testing.T) {
	t.Run("setup", func(t *testing.T) {
		installFakeProfiles(t, []string{"B"}, map[string]fakeProfile{"B": {url: statusServer(t, 200).URL}})
		dir := resetFileConfig(t)
		if err := runSetupWithInput(t, "MyTeam\n1\n"); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "esa-cli", "config.yml")); !strings.Contains(string(b), "team: myteam") {
			t.Errorf("setup が正規化した team を保存していない:\n%s", b)
		}
	})
	t.Run("config init", func(t *testing.T) {
		installFakeProfiles(t, []string{"B"}, map[string]fakeProfile{"B": {url: statusServer(t, 200).URL}})
		dir := resetFileConfig(t)
		var err error
		captureStdio(t, func() { err = configInit([]string{"-team", "MyTeam"}) })
		if err != nil {
			t.Fatalf("config init: %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "esa-cli", "config.yml")); !strings.Contains(string(b), "team: myteam") {
			t.Errorf("config init が正規化した team を保存していない:\n%s", b)
		}
	})
}

// resetFileConfig は XDG_CONFIG_HOME を空の一時ディレクトリへ向け、プロセス内で 1 回だけ読む
// config.yml のキャッシュ（loadFileConfig の sync.Once）を捨てる。捨てないと、先に走った
// テストが読んだ内容（開発者の実 config.yml のこともある）が残る。
func resetFileConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	reset := func() {
		fileConfigOnce = sync.Once{}
		fileConfigCached = fileConfig{}
		fileConfigErr = nil
	}
	reset()
	t.Cleanup(reset)
	return dir
}

// makeUnreadableProfile は Cookie DB を持つがディレクトリの権限で読めないプロファイルを作る。
func makeUnreadableProfile(t *testing.T, home, profile string) {
	t.Helper()
	writeCookieDB(t, home, profile, []fakeCookie{{Host: ".esa.io", Name: "_session", Value: "v"}})
	pd := profileDir(home, profile)
	if err := os.Chmod(pd, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pd, 0o700) })
	if _, err := os.Stat(filepath.Join(pd, "Network", "Cookies")); err == nil {
		t.Skip("権限 000 のディレクトリが読めてしまう環境（root 等）なので判定できない")
	}
}

// 権限で読めないプロファイル（chmod 000 / root 所有）は即停止にせず、後ろの正常なプロファイルを使う。
// 全部読めなければ、フルディスクアクセスとパーミッションの両方の案内を添える。
// 実物の buildClientForProfile を通す（読めるプロファイル B だけ httptest を向いた client に差し替える）。
func TestPermissionDeniedProfileIsSkippedAndHinted(t *testing.T) {
	t.Run("EACCES の後ろの正常なプロファイルを採用する", func(t *testing.T) {
		home := fakeChrome(t, nil)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		makeUnreadableProfile(t, home, "A")
		srv := statusServer(t, 200)
		origBuild, origScan := buildProfileClient, listScanProfiles
		t.Cleanup(func() { buildProfileClient, listScanProfiles = origBuild, origScan })
		listScanProfiles = func() []string { return []string{"A", "B"} }
		buildProfileClient = func(cfg config, p string) (*client, error) {
			if p == "B" {
				return testClient(srv.URL), nil
			}
			return buildClientForProfile(cfg, p) // 実物（A は権限で読めない）
		}
		var name string
		var err error
		captureStdio(t, func() { name, _, err = resolveProfileClient(config{team: "t", profile: profileAuto}) })
		if err != nil || name != "B" {
			t.Errorf("権限で読めないプロファイルで止まった: name=%q err=%v", name, err)
		}
	})

	t.Run("全プロファイルが EACCES なら権限の案内付きエラー", func(t *testing.T) {
		home := fakeChrome(t, nil)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		makeUnreadableProfile(t, home, "A")
		makeUnreadableProfile(t, home, "B")
		origScan := listScanProfiles
		t.Cleanup(func() { listScanProfiles = origScan })
		listScanProfiles = func() []string { return []string{"A", "B"} }
		_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
		if err == nil {
			t.Fatal("読めないのに成功した")
		}
		msg := err.Error()
		for _, want := range []string{`"A"`, `"B"`, "フルディスクアクセス", "パーミッション"} {
			if !strings.Contains(msg, want) {
				t.Errorf("エラーに %q が無い: %v", want, err)
			}
		}
	})

	t.Run("-profile 明示指定で EACCES なら権限の案内を添える", func(t *testing.T) {
		home := fakeChrome(t, nil)
		makeUnreadableProfile(t, home, "A")
		_, _, err := resolveProfileClient(config{team: "t", profile: "A"})
		if err == nil || !strings.Contains(err.Error(), "パーミッション") {
			t.Errorf("明示指定で権限の案内が無い: %v", err)
		}
	})

	t.Run("読めないだけ（非 perm）のときは権限の案内を出さない", func(t *testing.T) {
		home := fakeChrome(t, nil)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		path := writeCookieDB(t, home, "A", nil)
		if err := os.Mkdir(path+"-wal", 0o700); err != nil {
			t.Fatal(err)
		}
		origScan := listScanProfiles
		t.Cleanup(func() { listScanProfiles = origScan })
		listScanProfiles = func() []string { return []string{"A"} }
		_, _, err := resolveProfileClient(config{team: "t", profile: profileAuto})
		if err == nil || !strings.Contains(err.Error(), `"A"`) || strings.Contains(err.Error(), "パーミッション") {
			t.Errorf("非 perm の記録で権限の案内を出している / 理由が無い: %v", err)
		}
	})
}
