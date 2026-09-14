package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const userAgent = "esa-client (Chrome cookie session; internal doc reader)"

// client は <team>.esa.io の内部エンドポイントを Cookie セッションで叩く。
type client struct {
	http         *http.Client
	baseURL      string // https://<team>.esa.io
	cookieHeader string
}

func newClient(teamHost, cookieHeader string) *client {
	return &client{
		http:         &http.Client{Timeout: 30 * time.Second},
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

	b, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")

	switch resp.StatusCode {
	case http.StatusOK:
		// フォールスルーして本文検査へ
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "", &errSessionExpired{url: url}
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("見つかりません（404）: %s", url)
	case http.StatusTooManyRequests:
		return nil, "", fmt.Errorf("レート制限（429）: %s。しばらく待って再実行してください", url)
	default:
		return nil, "", fmt.Errorf("予期しないステータス %d: %s", resp.StatusCode, url)
	}

	// 200 でもログインページ HTML が返ることがある（Rails のセッション切れ）。
	if looksLikeLoginPage(b, ct) {
		return nil, "", &errSessionExpired{url: url}
	}
	return b, ct, nil
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
