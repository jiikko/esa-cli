package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// 送信先ホストの判定。ここが緩むと **無関係なサイトのセッションを New Relic へ送る**、
// 逆に厳しすぎると正当な Cookie を取りこぼしてログイン済みなのに認証が通らない。
//
// 敵対的レビューが試した形（公開サフィックス・部分一致・多重ドット・大文字）を
// そのまま回帰テーブルにしてある。
func TestCookieHostMatches(t *testing.T) {
	const req = "one.newrelic.com" // esa でも同じドメイン照合ロジックを使う
	cases := []struct {
		hostKey string
		want    bool
		why     string
	}{
		{hostKey: "one.newrelic.com", want: true, why: "host-only の完全一致"},
		{hostKey: ".newrelic.com", want: true, why: "ドメイン Cookie（サブドメインへ送られる）"},
		{hostKey: ".one.newrelic.com", want: true, why: "自分自身を含むドメイン Cookie"},
		{hostKey: "newrelic.com", want: false, why: "host-only は完全一致のみ（親ドメインは送らない）"},
		{hostKey: "two.newrelic.com", want: false, why: "別のサブドメイン"},
		{hostKey: "newrelic.com.evil.jp", want: false, why: "接尾辞の偽装"},
		{hostKey: ".newrelic.com.evil.jp", want: false, why: "接尾辞の偽装（ドメイン Cookie）"},
		{hostKey: "evil-one.newrelic.com", want: false, why: "ドット境界を跨がない部分一致"},
		{hostKey: ".com", want: true, why: "公開サフィックスは検査していない（既知の緩さ。下の注記参照）"},
		{hostKey: "", want: false, why: "空"},
		{hostKey: ".", want: false, why: "ドットのみ"},
		{hostKey: "..", want: false, why: "ドット 2 つ"},
		{hostKey: "..com", want: false, why: "多重ドット"},
		{hostKey: ".COM", want: false, why: "大文字（host_key は小文字で保存される）"},
		{hostKey: ".jp", want: false, why: "無関係な TLD"},
		{hostKey: "one.newrelic.com.", want: false, why: "末尾ドット"},
	}
	for _, c := range cases {
		if got := cookieHostMatches(c.hostKey, req); got != c.want {
			t.Errorf("cookieHostMatches(%q, %q) = %v, want %v（%s）", c.hostKey, req, got, c.want, c.why)
		}
	}
}

// buildCookieHeader は同名 Cookie が複数あるとき「より具体的なホスト」を採る。
//
// 🚨 ここが壊れると、別サブドメインの古い値でセッションを上書きして
// 「ログインしているのに 401」になる。壊れ方が認証エラーとして出るので原因が分かりにくい。
func TestBuildCookieHeaderPrefersMoreSpecificHost(t *testing.T) {
	// 🚨 正解（host-only）を先頭に置かないこと。
	// 先頭に置くと「最初に見つけたものを採る」実装でも通ってしまい、
	// 優先順位のロジックを壊す変異を検出できない（実測で素通りした）。
	// ここでは同じ rank になりうる .one.newrelic.com を先に置いてある。
	entries := []cookieEntry{
		{host: ".newrelic.com", name: "session", value: "domain-wide"},
		{host: ".one.newrelic.com", name: "session", value: "sub-domain"},
		{host: "one.newrelic.com", name: "session", value: "host-only"},
		{host: "evil.example.jp", name: "session", value: "unrelated"},
		{host: ".newrelic.com", name: "other", value: "keep"},
	}
	header, n := buildCookieHeader(entries, "one.newrelic.com")

	if n != 2 {
		t.Errorf("送るべき Cookie は 2 種類（session / other）: got %d（%q）", n, header)
	}
	if !strings.Contains(header, "session=host-only") {
		t.Errorf("host-only の Cookie を最優先すべき: %q", header)
	}
	for _, ng := range []string{"domain-wide", "sub-domain", "unrelated"} {
		if strings.Contains(header, ng) {
			t.Errorf("%q が混ざっている: %q", ng, header)
		}
	}
	if !strings.Contains(header, "other=keep") {
		t.Errorf("別名の Cookie が落ちている: %q", header)
	}

	// 無関係なホスト宛てには 1 つも送らない。
	if h, n := buildCookieHeader(entries, "example.com"); n != 0 || h != "" {
		t.Errorf("無関係なホストへ送ろうとしている: n=%d header=%q", n, h)
	}
}

// 鍵導出のパラメータを固定する。
//
// 期待値は Go の実装ではなく Python の hashlib で独立に算出したもの
// （production と同じ式で期待値を作ると、パラメータを変える変異を検出できない）。
// salt・反復回数・鍵長のどれが変わっても落ちる。
func TestDeriveKeyMatchesChromeParameters(t *testing.T) {
	cases := []struct {
		password string
		wantHex  string // python: hashlib.pbkdf2_hmac("sha1", pw, b"saltysalt", 1003, 16)
	}{
		{password: "peanuts", wantHex: "d9a09d499b4e1b7461f28e67972c6dbd"},
		{password: "testpassword", wantHex: "6fbfc7e7025290f47d9c2a84d67d5fd5"},
	}
	for _, c := range cases {
		key, err := deriveKey([]byte(c.password))
		if err != nil {
			t.Fatalf("deriveKey(%q): %v", c.password, err)
		}
		if got := hex.EncodeToString(key); got != c.wantHex {
			t.Errorf("deriveKey(%q) = %s, want %s（salt / 反復回数 / 鍵長のいずれかが変わっている）",
				c.password, got, c.wantHex)
		}
	}
}

// PKCS7 のパディング除去。不正なパディングを通すと、復号結果の末尾に
// ゴミが残った Cookie 値を送ることになる。
func TestPKCS7Unpad(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{name: "正常（4 バイト分）", data: append([]byte("abcdefghijkl"), 4, 4, 4, 4), want: "abcdefghijkl"},
		{name: "全部パディング", data: []byte{16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16}, want: ""},
		{name: "空", data: []byte{}, wantErr: true},
		{name: "ブロック長の倍数でない", data: []byte{1, 2, 3}, wantErr: true},
		{name: "パディングが 0", data: append([]byte("abcdefghijklmno"), 0), wantErr: true},
		{name: "パディングがブロック長超", data: append([]byte("abcdefghijklmno"), 17), wantErr: true},
		{name: "パディングバイトが揃っていない", data: append([]byte("abcdefghijkl"), 1, 2, 3, 4), wantErr: true},
	}
	for _, c := range cases {
		got, err := pkcs7Unpad(c.data, aes.BlockSize)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", c.name, err, c.wantErr)
			continue
		}
		if err == nil && string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// 復号の本体。暗号文はテスト側で stdlib を直接使って作る
// （production の暗号化関数は存在しないので、これが独立したオラクルになる）。
func TestDecryptValue(t *testing.T) {
	key, err := deriveKey([]byte("testpassword"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("v10 の暗号文を復号できる", func(t *testing.T) {
		enc := encryptForTest(t, key, []byte("session-value"))
		got, err := decryptValue(enc, key, 0, "")
		if err != nil {
			t.Fatalf("decryptValue: %v", err)
		}
		if got != "session-value" {
			t.Errorf("got %q, want %q", got, "session-value")
		}
	})

	t.Run("Chrome 130+ は先頭 32 バイトの SHA256(host_key) を照合して落とす", func(t *testing.T) {
		enc := encryptForTest(t, key, v24Plain(".esa.io", "session-value"))

		got, err := decryptValue(enc, key, 24, ".esa.io") // meta.version >= 24
		if err != nil {
			t.Fatalf("decryptValue: %v", err)
		}
		if got != "session-value" {
			t.Errorf("ハッシュプレフィックスが落ちていない: got %q", got)
		}

		// 古い Chrome（version < 24）では落としてはいけない。
		old, err := decryptValue(enc, key, 23, ".esa.io")
		if err != nil {
			t.Fatalf("decryptValue: %v", err)
		}
		if len(old) != 32+len("session-value") {
			t.Errorf("古い版で 32 バイトを落としている: len=%d", len(old))
		}

		// 先頭 32 バイトが別ホストのハッシュ / ゼロ埋めなら復号失敗。
		if _, err := decryptValue(enc, key, 24, "team.esa.io"); err == nil {
			t.Error("host_key が違うのにハッシュ照合を通した")
		}
		zero := encryptForTest(t, key, append(make([]byte, 32), []byte("session-value")...))
		if _, err := decryptValue(zero, key, 24, ".esa.io"); err == nil {
			t.Error("ゼロ埋めのプレフィックスを照合せずに通した")
		}
	})

	// 鍵違いでも PKCS7 の末尾は約 1/256 で偶然通り、v24 の長い値は 32 バイトを落としても残る。
	// 実際の件数（数千件）では照合が無いと数件が「復号成功」に紛れ、全件失敗の検出が働かない。
	t.Run("鍵違い・長い値・多数件で 1 件も成功にしない（v24）", func(t *testing.T) {
		other, err := deriveKey([]byte("wrongpassword"))
		if err != nil {
			t.Fatal(err)
		}
		const n = 3000
		var ok, unpadOK int
		for i := 0; i < n; i++ {
			enc := encryptForTest(t, key, v24Plain(".esa.io", strings.Repeat("x", 200+i%50)+strconv.Itoa(i)))
			if _, err := decryptValue(enc, other, 24, ".esa.io"); err == nil {
				ok++
			}
			if _, err := decryptValue(enc, other, 0, ""); err == nil {
				unpadOK++ // 照合の無い経路で偶然通った数（fixture が穴を再現できている証拠）
			}
		}
		if unpadOK == 0 {
			t.Fatalf("前提: 鍵違いで PKCS7 を偶然通る値が 1 件も無い（fixture が穴を再現していない）")
		}
		if ok != 0 {
			t.Errorf("鍵違いの %d/%d 件を復号成功にした（PKCS7 を偶然通った値: %d 件）", ok, n, unpadOK)
		}
	})

	t.Run("鍵が違うと元の値にならない", func(t *testing.T) {
		enc := encryptForTest(t, key, []byte("session-value"))
		other, err := deriveKey([]byte("wrongpassword"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := decryptValue(enc, other, 0, "")
		if err == nil && got == "session-value" {
			t.Error("違う鍵で復号できてしまった")
		}
	})

	t.Run("v10 でない値は平文として返す", func(t *testing.T) {
		got, err := decryptValue([]byte("plain-old-value"), key, 0, "")
		if err != nil {
			t.Fatalf("decryptValue: %v", err)
		}
		if got != "plain-old-value" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("空の値", func(t *testing.T) {
		got, err := decryptValue(nil, key, 0, "")
		if err != nil || got != "" {
			t.Errorf("got %q err=%v", got, err)
		}
	})

	t.Run("ブロック長に合わない暗号文はエラー", func(t *testing.T) {
		if _, err := decryptValue([]byte("v10abc"), key, 0, ""); err == nil {
			t.Error("エラーになるべき")
		}
	})

	t.Run("復号結果がハッシュプレフィックスより短いとエラー", func(t *testing.T) {
		enc := encryptForTest(t, key, []byte("short")) // 32 バイト未満
		if _, err := decryptValue(enc, key, 24, ".esa.io"); err == nil {
			t.Error("エラーになるべき")
		}
	})
}

// v24Plain は DB version 24 以上の平文（SHA256(host_key) + 値）を作る。
func v24Plain(hostKey, value string) []byte {
	h := sha256.Sum256([]byte(hostKey))
	return append(h[:], []byte(value)...)
}

// encryptForTest は Chrome と同じ形式（v10 + AES-128-CBC + IV=0x20*16 + PKCS7）で暗号化する。
// production 側に暗号化の実装は無いので、これは独立したオラクルとして働く。
func encryptForTest(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, 0, len(plain)+pad)
	padded = append(padded, plain...)
	for i := 0; i < pad; i++ {
		padded = append(padded, byte(pad))
	}
	iv := []byte("                ") // 0x20 * 16
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return append([]byte("v10"), out...)
}
