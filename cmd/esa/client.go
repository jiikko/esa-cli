package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const userAgent = "esa-cli (Chrome cookie session; read-only)"

// maxResponseBytes はレスポンス本文の上限。超えたら切り詰めずにエラーにする
// （切り詰めた Markdown / JSON を正常な結果として出さないため）。
const maxResponseBytes = 20 * 1024 * 1024

// errRedirectBlocked は scheme / ホストの変わるリダイレクトを止めたことを示す。
// 未ログインの esa は SSO 等の別ホストへ飛ばすことがあるため、authOK はこれを未認証として扱う。
type errRedirectBlocked struct{ msg string }

func (e *errRedirectBlocked) Error() string { return e.msg }

// errTooManyRedirects は同一ホスト内のリダイレクトが上限を超えたことを示す（ループ等）。
type errTooManyRedirects struct{ n int }

func (e *errTooManyRedirects) Error() string {
	return fmt.Sprintf("リダイレクトが多すぎます（%d 回）", e.n)
}

// errHTTPStatus は 200 / 401 / 403 / 404 以外のステータスを受け取ったことを示す。
// authOK はコードで「チーム全体の失敗（429・5xx）」と「プロファイル固有の失敗（その他）」を分ける。
type errHTTPStatus struct {
	code int
	url  string
}

func (e *errHTTPStatus) Error() string {
	if e.code == http.StatusTooManyRequests {
		return fmt.Sprintf("レート制限（429）: %s。しばらく待って再実行してください", e.url)
	}
	return fmt.Sprintf("予期しないステータス %d: %s", e.code, e.url)
}

// fetchConcurrency は記事の JSON を並列に取るときの並列度（search の補完・sync の取得）。
const fetchConcurrency = 6

// sharedTransport は全クライアントが共有する接続プール。
//
// 🚨 http.DefaultTransport をそのまま使わない。ホストごとに待機させておける接続が 2 本
// （MaxIdleConnsPerHost の既定）しかなく、fetchConcurrency 並列で取ると残りの接続は毎回閉じられて
// 新しく張り直される。閉じた接続は TIME_WAIT で一時ポートを占有し、続けて回すとポートが尽きて
// `connect: can't assign requested address` で全件失敗した（issue 012。テストの繰り返し実行で実測）。
// 並列度から決めるので、並列度だけを上げて使い回しが外れる形にはならない。
var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = fetchConcurrency
	return t
}()

// newHTTPClient は資格情報を持ち越さないリダイレクト方針を持つクライアントを作る。
//
// 🚨 Go の既定はリダイレクトを追い、そのとき資格情報が持ち越される:
//   - Cookie / Authorization は**別ドメインへは剥がれる**が、
//     **https→http のダウングレードでは剥がれない**（stdlib はホスト名しか比較せず
//     scheme を見ない）。セッションや API トークンが平文で線に乗る
//
// esa は GET で記事を読むだけなので、同一ホスト内の https リダイレクトは追ってよい。
// scheme のダウングレードとホストの変更だけを止める。
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: sharedTransport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			orig := via[0].URL
			if req.URL.Scheme != orig.Scheme {
				return &errRedirectBlocked{fmt.Sprintf("リダイレクト先の scheme が変わりました（%s → %s）。資格情報を送らずに中止します",
					orig.Scheme, req.URL.Scheme)}
			}
			if req.URL.Host != orig.Host {
				return &errRedirectBlocked{fmt.Sprintf("リダイレクト先のホストが変わりました（%s → %s）。資格情報を送らずに中止します",
					orig.Host, req.URL.Host)}
			}
			if len(via) >= 5 {
				return &errTooManyRedirects{n: len(via)}
			}
			return nil
		},
	}
}

// client は <team>.esa.io の内部エンドポイントを Cookie セッションで叩く。
type client struct {
	http         *http.Client
	baseURL      string // https://<team>.esa.io
	cookieHeader string
}

func newClient(teamHost, cookieHeader string) *client {
	return &client{
		http:         newHTTPClient(),
		baseURL:      "https://" + teamHost,
		cookieHeader: cookieHeader,
	}
}

// errSessionExpired はログインページ（200 だが未認証）を受け取ったことを示す。
type errSessionExpired struct {
	url string
}

func (e *errSessionExpired) Error() string {
	return fmt.Sprintf(
		"セッションが無効です（%s がログインページを返しました）。\n"+
			"  Chrome で https://%s にログインし直してから再実行してください。",
		e.url, strings.TrimPrefix(strings.TrimPrefix(e.url, "https://"), "http://"))
}

// errNotFound は 404 を受け取ったことを示す。
//
// 🚨 非公開チームでは未認証のとき全パスが 404 になるため、authOK はこれを
// 「未ログイン」として扱う（型で判定する。メッセージ文字列で判定しない）。
type errNotFound struct {
	url string
}

func (e *errNotFound) Error() string { return fmt.Sprintf("見つかりません（404）: %s", e.url) }

// get は path を GET し、生のレスポンスボディと Content-Type を返す。
// ステータス・ログインHTML・エラー JSON をここで検出する。
func (c *client) get(path string) (body []byte, contentType string, err error) {
	url := c.baseURL + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Cookie", c.cookieHeader)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json,application/xhtml+xml")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("リクエスト失敗（%s）: %w", url, err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")

	switch resp.StatusCode {
	case http.StatusOK:
		// フォールスルーして本文検査へ
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "", &errSessionExpired{url: url}
	case http.StatusNotFound:
		return nil, "", &errNotFound{url: url}
	default: // 429 を含む
		return nil, "", &errHTTPStatus{code: resp.StatusCode, url: url}
	}

	b, err := readLimitedBody(resp.Body, url)
	if err != nil {
		return nil, "", err
	}

	// 200 でもログインページ HTML が返ることがある（Rails のセッション切れ）。
	if looksLikeLoginPage(b, ct) {
		return nil, "", &errSessionExpired{url: url}
	}
	return b, ct, nil
}

// readLimitedBody は本文を上限まで読む。上限 +1 まで読み、超えたら切り詰めずにエラーにする。
func readLimitedBody(r io.Reader, url string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("レスポンスの読み取りに失敗（%s）: %w", url, err)
	}
	if len(b) > maxResponseBytes {
		return nil, fmt.Errorf("レスポンスがサイズ上限（%d MiB）を超えました: %s", maxResponseBytes>>20, url)
	}
	return b, nil
}

func rateLimitError(url string) error {
	return &errHTTPStatus{code: http.StatusTooManyRequests, url: url}
}

// getJSON は path を GET し JSON をデコードする。{"error":...} も検出する。
func (c *client) getJSON(path string, v any) error {
	b, ct, err := c.get(path)
	if err != nil {
		return err
	}
	if !strings.Contains(ct, "json") && (len(b) == 0 || (b[0] != '{' && b[0] != '[')) {
		return &errSessionExpired{url: c.baseURL + path}
	}
	// esa のエラー形式 {"error": "...", "message": "..."}
	var probe struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &probe) == nil && probe.Error != "" {
		return fmt.Errorf("esa エラー: %s (%s)", probe.Error, probe.Message)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("JSON デコード失敗（%s）: %w", path, err)
	}
	return nil
}

// looksLikeLoginPage は本文が未認証時のログイン/トップ HTML かを判定する。
func looksLikeLoginPage(body []byte, contentType string) bool {
	if !strings.Contains(contentType, "html") {
		return false
	}
	s := strings.ToLower(string(body[:min(len(body), 8192)]))
	// esa 未ログイン時に現れる典型的なマーカー。
	markers := []string{
		"/users/sign_in",
		"sign in with",
		"action=\"/users/sign_in\"",
		"class=\"sign-in",
		"esa へようこそ",
	}
	for _, m := range markers {
		if strings.Contains(s, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// postMD は /posts/:n.md を取得する（YAML front matter 付き Markdown）。
func (c *client) postMD(number int) (string, error) {
	b, ct, err := c.get(fmt.Sprintf("/posts/%d.md", number))
	if err != nil {
		return "", err
	}
	// .md なのに HTML が返ったらセッション切れ扱い。
	if strings.Contains(ct, "html") || looksLikeHTMLBody(b) {
		return "", &errSessionExpired{url: fmt.Sprintf("%s/posts/%d.md", c.baseURL, number)}
	}
	return string(b), nil
}

func looksLikeHTMLBody(b []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(b[:min(len(b), 512)])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// postJSON は /posts/:n.json を取得して汎用 map を返す。
func (c *client) postJSON(number int, includeComments bool) (map[string]any, error) {
	path := fmt.Sprintf("/posts/%d.json", number)
	if includeComments {
		path += "?include=comments"
	}
	var v map[string]any
	if err := c.getJSON(path, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// revisionsJSON は /posts/:n/revisions.json を取得する。
func (c *client) revisionsJSON(number int) (map[string]any, error) {
	var v map[string]any
	if err := c.getJSON(fmt.Sprintf("/posts/%d/revisions.json", number), &v); err != nil {
		return nil, err
	}
	return v, nil
}
