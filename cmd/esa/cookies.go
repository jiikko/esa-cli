// Google Chrome の Cookie を macOS Keychain 経由で復号して取り出す。
//
// 仕様（PBKDF2-SHA1 1003 回 / AES-128-CBC / IV=0x20*16 / Chrome 130+ =
// meta.version>=24 で復号後の先頭 32 バイトが SHA256(host_key)）は
// github.com/jiikko/newrelic-nrql-cli の cmd/nrql/cookies.go と共通なので、
// 修正が要る場合は両方に当てること。
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"
)

// 🚨 このツールは Google Chrome 専用。
//
// 以前は Brave / Chromium / Edge / Vivaldi も対応表に持っていたが、意図的に
// Chrome だけへ絞った（issue 003）。理由は、対応表の値（Keychain のサービス名・
// Application Support 配下のディレクトリ名）は**実機で確認しないと正しいか
// 分からない**ため。手元で確認できるのは Chrome だけで、未確認の値を並べると
// 「動くように見えて別ブラウザの領域を読みに行く」形の事故になる。
//
// 他のブラウザに広げるなら、実機で `security find-generic-password` と
// Application Support 配下のディレクトリ名を確認してから足すこと。
const (
	chromeKeychainAccount = "Chrome"              // security -a
	chromeKeychainService = "Chrome Safe Storage" // security -s
	chromeSupportSubdir   = "Google/Chrome"       // ~/Library/Application Support 配下
	chromeName            = "Google Chrome"       // エラーメッセージ用の表示名
)

// cookieEntry は復号済みの 1 Cookie。
type cookieEntry struct {
	host  string // host_key（先頭ドットを含む場合がある）
	name  string
	value string
}

// keychainPassword はテストの差し替え口（seam）。production では常に getKeychainPassword。
// 実物は security を起動して Keychain を読むため、テストからは fake に差し替える。
var keychainPassword = getKeychainPassword

// getKeychainPassword は Keychain から "Chrome Safe Storage" のパスワードを取得する。
func getKeychainPassword() ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-w",
		"-a", chromeKeychainAccount,
		"-s", chromeKeychainService)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf(
			"Keychain から暗号化キーを取得できませんでした（service=%q account=%q）。\n"+
				"  - ターミナルで次を実行し、表示される許可ダイアログで「常に許可」を押してください:\n"+
				"      security find-generic-password -w -a %q -s %q\n"+
				"  - %s がインストールされ、ログインしているか確認してください（このツールは Chrome 専用です）。\n"+
				"  元エラー: %w",
			chromeKeychainService, chromeKeychainAccount,
			chromeKeychainAccount, chromeKeychainService, chromeName, err)
	}
	return []byte(strings.TrimRight(string(out), "\n")), nil
}

// deriveKey は PBKDF2-SHA1（macOS: 1003 回, 16 バイト）で AES-128 鍵を導出する。
func deriveKey(password []byte) ([]byte, error) {
	return pbkdf2.Key(sha1.New, string(password), []byte("saltysalt"), 1003, 16)
}

// decryptValue は encrypted_value を復号する。
// v10 プレフィックスなら AES-128-CBC（IV=0x20*16, PKCS7）で復号し、
// metaVersion>=24 なら復号後の先頭 32 バイト（ホストハッシュ）を照合してから落とす。
//
// 🚨 v24 以上では、先頭 32 バイトが SHA256(host_key) と一致することを確かめる。
// 根拠: Chromium の net/extras/sqlite/sqlite_persistent_cookie_store.cc は DB version 24 から
// 暗号化前の平文の先頭に crypto::SHA256HashString(<cookie の domain = host_key 列の値>) を付け、
// 読み出し時に同じ値と比べて一致しなければ復号失敗として扱う（domain hash prefix）。
// host_key は DB に保存されたとおりの値（先頭ドットを含む。例 ".esa.io"）を渡すこと。
// 照合しないと、鍵違いでも PKCS7 の末尾が約 1/256 の確率で偶然通り、v24 の長い値は
// 32 バイトを落としても残るため、多数件の DB では「全件復号失敗」の検出が働かない。
// この照合は v24 以上にしか効かない。v23 以前の平文には照合できる情報が無いので、鍵違いでも
// PKCS7 を偶然通った値は成功に数えられる（全件失敗の検出は「偶然通った値が 0 件」のときだけ働く）。
// 照合を誤ると（host_key の加工・別のハッシュ）正常な Cookie が全部復号失敗になるので、
// 変えるときは実機の Cookie DB で確認すること。
func decryptValue(enc, key []byte, metaVersion int, hostKey string) (string, error) {
	if len(enc) == 0 {
		return "", nil
	}
	if len(enc) < 3 || string(enc[:3]) != "v10" {
		// 古い Chrome の平文データ
		return string(enc), nil
	}
	ciphertext := enc[3:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("暗号文の長さが不正です")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := []byte("                ") // 0x20 * 16
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(ciphertext))
	mode.CryptBlocks(plain, ciphertext)

	// PKCS7 unpad
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}
	if metaVersion >= 24 {
		if len(plain) < sha256.Size {
			return "", errors.New("復号結果がハッシュプレフィックスより短いです")
		}
		want := sha256.Sum256([]byte(hostKey))
		if !bytes.Equal(plain[:sha256.Size], want[:]) {
			return "", errors.New("復号結果のホストハッシュが一致しません（鍵が違う可能性）")
		}
		plain = plain[sha256.Size:]
	}
	return string(plain), nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("PKCS7: データ長が不正です")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("PKCS7: パディングが不正です")
	}
	// 🚨 最終バイトだけでなく、パディング全体が同じ値であることを確かめる。
	// 最終バイトしか見ない実装は 1,2,3,4 のような不正なパディングを通し、
	// 復号結果の末尾にゴミが残った Cookie 値をそのまま送ることになる。
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("PKCS7: パディングバイトが揃っていません")
		}
	}
	return data[:len(data)-pad], nil
}

// cookieDBSourcePath は Cookie DB の絶対パスを返す（cwd 非依存: HOME 起点）。
func cookieDBSourcePath(profile string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// 新しい Chrome は Cookies を Network/ サブディレクトリに置く。両方を候補にする。
	base := filepath.Join(home, "Library", "Application Support", chromeSupportSubdir, profile)
	candidates := []string{
		filepath.Join(base, "Network", "Cookies"),
		filepath.Join(base, "Cookies"),
	}
	for _, c := range candidates {
		_, err := os.Stat(c)
		if err == nil {
			return c, nil
		}
		// 🚨 「無い」(ENOENT) だけを次の候補へ進める。それ以外（権限・I/O）は「見つからない」に
		// 化けさせず、理由を記録して次のプロファイルへ進む（skip + 記録）。
		// 権限（EACCES / EPERM）を即停止にしないこと: chmod 000 や root 所有（sudo で起動した
		// Chrome が作ったもの）のプロファイルはプロファイル固有で、フルディスクアクセスを付けても直らない。
		if !errors.Is(err, fs.ErrNotExist) {
			return "", accessSkipError(profile, "確認できません", c, err)
		}
	}
	// プロファイル固有の欠落なので、自動検出は次のプロファイルへ進んでよい（profile.go）。
	return "", &profileSkipError{msg: fmt.Sprintf(
		"Cookie DB が見つかりませんでした（プロファイル=%q）。探した場所:\n  %s\n"+
			"  - プロファイル名が正しいか確認してください（-profile / ESA_CHROME_PROFILE）。\n"+
			"  - ~/Library/Application Support/%s/ 配下のディレクトリ名がプロファイル名です（既定は Default）。",
		profile, strings.Join(candidates, "\n  "), chromeSupportSubdir)}
}

// accessSkipError は Cookie DB（本体 / -wal / -shm）を stat・読み取りできなかったことを表す
// skip + 記録のエラー。権限（EACCES / EPERM）なら perm を立て、最終的に認証済みプロファイルが
// 見つからなかったときに権限の案内（profile.go の permissionHint）を添えさせる。
func accessSkipError(profile, what, path string, err error) error {
	reason := "読み取りエラー"
	perm := errors.Is(err, fs.ErrPermission)
	if perm {
		reason = "アクセス拒否"
	}
	return &profileSkipError{
		msg:    fmt.Sprintf("プロファイル %q の Cookie DB を%s（%s）: %s: %v", profile, what, reason, path, err),
		broken: true,
		perm:   perm,
	}
}

// --- 一時コピーの後始末 ---
//
// 🚨 「必ず消す」を defer だけに任せない。Go の既定ではシグナル（Ctrl-C）で
// プロセスが即終了して defer が走らず、Chrome の Cookie DB の完全なコピーが
// $TMPDIR に残る。macOS のフルディスクアクセスで守られた領域の中身を、
// 守られていない場所へ置き去りにすることになる。
//
// 3 段構え（段ごとにテストを持つ。cookies_cleanup_test.go）:
//  ① defer による即時削除 — 正常終了・エラー・panic を覆う
//  ② シグナル（SIGINT/SIGTERM/SIGHUP）を捕まえ、登録済みの削除を実行してから終了する
//  ③ ②でも間に合わない終わり方（SIGKILL・強制終了・電源断）に備え、起動時に
//     「自分が作った親ディレクトリ直下」「名前が <pid>-… の形」「その pid が生きていない」の
//     3 条件をすべて満たすものだけを消す
//
// ③ は破壊的操作なので、条件に合わないものは一切触らない（母集合を広げない）。

var (
	cleanupMu    sync.Mutex
	cleanupPaths = map[string]struct{}{}
)

// registerCleanup は「プロセスが終わる前に消すべきパス」を登録する（②が使う）。
func registerCleanup(path string) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	cleanupPaths[path] = struct{}{}
}

// runAllCleanups は登録済みのパスをすべて削除する。
// ①の defer と②のシグナル経路の両方から呼ばれるが、os.RemoveAll は冪等なので二重呼び出しは無害。
func runAllCleanups() {
	cleanupMu.Lock()
	paths := make([]string, 0, len(cleanupPaths))
	for p := range cleanupPaths {
		paths = append(paths, p)
	}
	cleanupPaths = map[string]struct{}{}
	cleanupMu.Unlock()
	for _, p := range paths {
		_ = os.RemoveAll(p)
	}
}

// installCleanupOnSignal は②を仕掛ける。main の先頭で 1 回だけ呼ぶ。
func installCleanupOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-ch
		runAllCleanups()
		if s, ok := sig.(syscall.Signal); ok {
			os.Exit(128 + int(s)) // シェルの慣習（SIGINT=130 / SIGTERM=143）
		}
		os.Exit(1)
	}()
}

// cookieTempRoot は一時コピーの親ディレクトリ。
// 自分だけが作る固定パスにすることで、③の掃除対象を「この配下」に閉じ込める。
func cookieTempRoot() string {
	return filepath.Join(os.TempDir(), "esa-cookie")
}

// ensureCookieTempRoot は親ディレクトリを 0700 で用意する。
// 他人が用意した同名のディレクトリ・シンボリックリンクだった場合は使わずに失敗する
// （その配下を消しに行くのは③なので、所有者の確認をここで済ませる）。
func ensureCookieTempRoot() (string, error) {
	root := cookieTempRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("一時ディレクトリが通常のディレクトリではありません: %s", root)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("一時ディレクトリの所有者が自分ではありません: %s", root)
	}
	return root, nil
}

// sweepStaleCookieDirs は③。過去の実行が SIGKILL 等で残したものだけを消す。
// 条件に 1 つでも合わなければ触らない（判断できないものは残す方へ倒す）。
func sweepStaleCookieDirs() {
	root := cookieTempRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return // 親ディレクトリが無ければ何もしない
	}
	self := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() {
			continue // ディレクトリ以外は対象外
		}
		pid, ok := pidFromTempDirName(e.Name())
		if !ok || pid == self {
			// 名前の形が違う / 自分のものは触らない。
			// 🚨 この !ok と processAlive の pid<=0 ガードは現状「互いを覆う」冗長な関係にある
			//（形が違う → pid 0 → 判定不能 → 消さない）。片方を外す変異は素通りするので、
			// 外すときは「もう片方が本当に同じものを守るか」を確かめること。意図的に両方残す。
			continue
		}
		if processAlive(pid) {
			continue // 生きているプロセスのものは触らない（並行実行）
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// pidFromTempDirName は "<pid>-<乱数>" 形式のディレクトリ名から pid を取り出す。
// この形式でないものは対象外（false を返す）。
func pidFromTempDirName(name string) (int, bool) {
	i := strings.IndexByte(name, '-')
	if i <= 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(name[:i])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive は pid のプロセスが生きているかを返す。
// 判断できないとき（権限が無い等）は「生きている」に倒す = 消さない方へ倒す。
//
// 🚨 os.FindProcess + Process.Signal を使わないこと。消えたプロセスに対して
// ESRCH ではなく os.ErrProcessDone を返すため、ESRCH だけを見る判定は
// 「常に生きている」に落ちて掃除が 1 件も走らなくなる（実測で踏んだ）。
// kill(2) を直接呼び、errno をそのまま判定する。
func processAlive(pid int) bool {
	// 🚨 pid 0 / 負値を kill(2) に渡さない。0 は「自分のプロセスグループ全体」、
	// 負値は「プロセスグループ指定」を意味し、生死判定にならない（成功して
	// 「生きている」に見える）。ここでは判定不能として扱い、消さない方へ倒す。
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true // シグナルを送れた = 生きている
	}
	if errors.Is(err, syscall.ESRCH) {
		return false // そんなプロセスは無い = 死んでいる
	}
	return true // EPERM 等、判断できないときは消さない
}

// copyCookieDB は Cookie DB を一時ディレクトリへコピーする。
// WAL に未反映のセッション Cookie を取りこぼさないよう、-wal / -shm も同名でコピーする。
// 返り値: 一時 DB パスと後始末関数。
func copyCookieDB(profile, src string) (string, func(), error) {
	root, err := ensureCookieTempRoot()
	if err != nil {
		return "", nil, err
	}
	// ディレクトリ名に pid を埋める（③がこれを見て「生きていない実行の残骸」を判定する）。
	tmpdir, err := os.MkdirTemp(root, fmt.Sprintf("%d-", os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	registerCleanup(tmpdir)
	cleanup := func() { os.RemoveAll(tmpdir) }

	for _, suffix := range []string{"", "-wal", "-shm"} {
		s := src + suffix
		data, err := os.ReadFile(s)
		if err != nil {
			// 🚨 -wal / -shm は「無い」(ENOENT) ときだけ飛ばす。権限・I/O エラーで飛ばすと、
			// WAL にしか無いセッション Cookie が落ちて「Cookie 0 件」→ 黙って skip に化ける。
			// それ以外の失敗は本体と同じく skip + 記録（プロファイル固有。即停止にはしない）。
			if suffix != "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			cleanup()
			return "", nil, accessSkipError(profile, "読み取れません", s, err)
		}
		dst := filepath.Join(tmpdir, "Cookies"+suffix)
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return filepath.Join(tmpdir, "Cookies"), cleanup, nil
}

// extractCookies は指定プロファイル（Chrome）から全 Cookie を復号して返す。
func extractCookies(profile string) ([]cookieEntry, error) {
	sweepStaleCookieDirs() // ③: 前回の実行が強制終了で残したものを先に片付ける

	password, err := keychainPassword()
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(password)
	if err != nil {
		return nil, err
	}

	src, err := cookieDBSourcePath(profile)
	if err != nil {
		return nil, err
	}
	dbPath, cleanup, err := copyCookieDB(profile, src)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// ここから先の失敗（壊れた DB・sqlite 以外のファイル・古いスキーマ・全件の復号失敗）は
	// Keychain の鍵を取れた後の**このプロファイル固有**の問題なので、skip + 理由の記録にする
	// （1 つ壊れたプロファイルで自動検出全体を止めない。profile.go の profileSkipError）。
	broken := func(format string, a ...any) error {
		return &profileSkipError{msg: fmt.Sprintf("プロファイル %q の Cookie DB を読めません: ", profile) + fmt.Sprintf(format, a...), broken: true}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, broken("%v", err)
	}
	defer db.Close()

	var metaVersion int
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'version'`).Scan(&metaVersion); err != nil {
		// meta が読めない場合は 0 扱い（トリム無し）で続行
		metaVersion = 0
	}

	rows, err := db.Query(`SELECT host_key, name, value, encrypted_value FROM cookies`)
	if err != nil {
		return nil, broken("cookies テーブルの読み取りに失敗: %v", err)
	}
	defer rows.Close()

	var out []cookieEntry
	var decryptTried, decryptOK int
	var firstDecryptErr error
	for rows.Next() {
		var host, name, plainValue string
		var enc []byte
		if err := rows.Scan(&host, &name, &plainValue, &enc); err != nil {
			return nil, broken("%v", err)
		}
		value := plainValue
		if value == "" && len(enc) > 0 {
			decryptTried++
			v, derr := decryptValue(enc, key, metaVersion, host)
			if derr != nil {
				if firstDecryptErr == nil {
					firstDecryptErr = derr
				}
				continue // 1 件の復号失敗で全体を止めない
			}
			decryptOK++
			value = v
		}
		out = append(out, cookieEntry{host: host, name: name, value: value})
	}
	if err := rows.Err(); err != nil {
		return nil, broken("%v", err)
	}
	// 🚨 全件失敗を「Cookie 0 件」に化けさせない（鍵違い等。1 件だけの失敗は従来どおり続行）。
	if decryptTried > 0 && decryptOK == 0 {
		return nil, broken("暗号化された Cookie %d 件の復号にすべて失敗しました（Keychain の鍵が合っていない可能性）: %v",
			decryptTried, firstDecryptErr)
	}
	return out, nil
}

// cookieHostMatches は Cookie の host_key が対象ホストに送信されるべきか判定する。
// 標準の Cookie ドメインマッチ（ドット境界）を用い、suffix 文字列比較の誤爆を避ける。
func cookieHostMatches(hostKey, reqHost string) bool {
	if hostKey == "" {
		return false
	}
	if strings.HasPrefix(hostKey, ".") {
		d := hostKey[1:] // domain cookie
		return reqHost == d || strings.HasSuffix(reqHost, "."+d)
	}
	return hostKey == reqHost // host-only cookie は完全一致のみ
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
		if !cookieHostMatches(c.host, reqHost) {
			continue
		}
		rank := len(strings.TrimPrefix(c.host, "."))
		if !strings.HasPrefix(c.host, ".") {
			rank += 1000 // host-only を最優先
		}
		if cur, ok := best[c.name]; !ok || rank > cur.rank {
			best[c.name] = pick{value: c.value, rank: rank}
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
