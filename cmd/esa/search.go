package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// searchResult は検索ヒット 1 件。基本情報（number/title/url）は検索 HTML から、
// 日付・author 等の詳細は各記事の JSON で enrich して埋める。
type searchResult struct {
	Number    int      `json:"number"`
	FullName  string   `json:"full_name"`          // カテゴリ/タイトル
	Title     string   `json:"name"`               // タイトルのみ
	Category  string   `json:"category,omitempty"` // カテゴリ
	URL       string   `json:"url"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	CreatedBy string   `json:"created_by,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	Wip       *bool    `json:"wip,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

var errParseFailed = fmt.Errorf("検索結果から記事を抽出できませんでした（HTML 構造が変わった可能性があります）")

// search は検索を実行する。ESA_TOKEN があれば公式 API（JSON, 一括で詳細まで取得）、
// なければ内部エンドポイントの HTML から番号を取り、各記事 JSON で enrich する。
func (c *client) search(teamName, query string, perPage, page int, token string, enrich bool) ([]searchResult, error) {
	if token != "" {
		return searchViaAPI(teamName, query, perPage, page, token)
	}
	results, err := c.searchViaHTML(query, page)
	if err != nil {
		return nil, err
	}
	if enrich {
		c.enrichResults(results, 6)
	}
	return results, nil
}

// searchViaAPI は api.esa.io の公式 API を使う（ESA_TOKEN 指定時のみ）。詳細まで一括で返る。
func searchViaAPI(teamName, query string, perPage, page int, token string) ([]searchResult, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	api := fmt.Sprintf("https://api.esa.io/v1/teams/%s/posts?%s", teamName, q.Encode())

	req, _ := http.NewRequest(http.MethodGet, api, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)

	hc := newHTTPClient() // 資格情報を持ち越さないリダイレクト方針（client.go）
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("公式 API リクエスト失敗: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("公式 API: トークンが無効です（401）。ESA_TOKEN を確認してください")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("公式 API: 予期しないステータス %d", resp.StatusCode)
	}
	var out struct {
		Posts []struct {
			Number    int      `json:"number"`
			Name      string   `json:"name"`
			FullName  string   `json:"full_name"`
			Category  string   `json:"category"`
			URL       string   `json:"url"`
			Wip       bool     `json:"wip"`
			Tags      []string `json:"tags"`
			CreatedAt string   `json:"created_at"`
			UpdatedAt string   `json:"updated_at"`
			CreatedBy struct {
				ScreenName string `json:"screen_name"`
			} `json:"created_by"`
			UpdatedBy struct {
				ScreenName string `json:"screen_name"`
			} `json:"updated_by"`
		} `json:"posts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("公式 API: JSON デコード失敗: %w", err)
	}
	results := make([]searchResult, 0, len(out.Posts))
	for _, p := range out.Posts {
		wip := p.Wip
		results = append(results, searchResult{
			Number: p.Number, Title: stdhtml.UnescapeString(p.Name),
			FullName: stdhtml.UnescapeString(p.FullName), Category: stdhtml.UnescapeString(p.Category), URL: p.URL,
			CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
			CreatedBy: p.CreatedBy.ScreenName, UpdatedBy: p.UpdatedBy.ScreenName,
			Wip: &wip, Tags: p.Tags,
		})
	}
	return results, nil
}

// searchViaHTML は内部エンドポイント /posts?q=... の HTML から記事番号を抽出する。
func (c *client) searchViaHTML(query string, page int) ([]searchResult, error) {
	q := url.Values{}
	q.Set("q", query)
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	body, ct, err := c.get("/posts?" + q.Encode())
	if err != nil {
		return nil, err
	}
	if !strings.Contains(ct, "html") {
		return nil, fmt.Errorf("検索: HTML 以外が返りました（Content-Type=%q）", ct)
	}

	results, err := parseSearchHTML(body, c.baseURL)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if isEmptyResultPage(body) {
			return []searchResult{}, nil // 明示的な 0 件
		}
		return nil, errParseFailed // 抽出失敗（セレクタ変更の疑い）
	}
	return results, nil
}

var postHrefRe = regexp.MustCompile(`/posts/(\d+)`)

// parseSearchHTML は検索結果 HTML から post-title__link の anchor を抽出する。
func parseSearchHTML(body []byte, baseURL string) ([]searchResult, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("検索 HTML のパースに失敗: %w", err)
	}

	var results []searchResult
	seen := map[int]bool{}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && attr(n, "class") == "post-title__link" {
			href := attr(n, "href")
			if m := postHrefRe.FindStringSubmatch(href); m != nil {
				num, _ := strconv.Atoi(m[1])
				if !seen[num] {
					seen[num] = true
					title := stdhtml.UnescapeString(strings.TrimSpace(spanText(n, "post-title__name")))
					results = append(results, searchResult{
						Number:   num,
						Title:    title,
						FullName: title, // enrich 前の暫定値（enrich で正式名に上書き）
						URL:      absoluteURL(baseURL, href),
					})
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	return results, nil
}

// enrichResults は各記事の /posts/N.json を並行取得し、日付・author 等を埋める。
func (c *client) enrichResults(results []searchResult, concurrency int) {
	if len(results) == 0 {
		return
	}
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			post, err := c.postJSON(results[i].Number, false)
			if err != nil {
				return // 取得失敗した記事は基本情報のみで残す
			}
			applyPostJSON(&results[i], post)
		}(i)
	}
	wg.Wait()
}

// applyPostJSON は /posts/N.json のフィールドを searchResult に反映する。
func applyPostJSON(r *searchResult, post map[string]any) {
	// esa は name/full_name 内の "/" を &#47;、"#" を &#35; にエスケープして返すため復元する。
	if v, ok := post["full_name"].(string); ok && v != "" {
		r.FullName = stdhtml.UnescapeString(v)
	}
	if v, ok := post["name"].(string); ok && v != "" {
		r.Title = stdhtml.UnescapeString(v)
	}
	if v, ok := post["category"].(string); ok {
		r.Category = stdhtml.UnescapeString(v)
	}
	if v, ok := post["created_at"].(string); ok {
		r.CreatedAt = v
	}
	if v, ok := post["updated_at"].(string); ok {
		r.UpdatedAt = v
	}
	if v, ok := post["wip"].(bool); ok {
		r.Wip = &v
	}
	if by, ok := post["created_by"].(map[string]any); ok {
		if sn, ok := by["screen_name"].(string); ok {
			r.CreatedBy = sn
		}
	}
	if by, ok := post["updated_by"].(map[string]any); ok {
		if sn, ok := by["screen_name"].(string); ok {
			r.UpdatedBy = sn
		}
	}
	if ts, ok := post["tags"].([]any); ok {
		r.Tags = r.Tags[:0]
		for _, t := range ts {
			if s, ok := t.(string); ok {
				r.Tags = append(r.Tags, s)
			}
		}
	}
}

// isEmptyResultPage は「検索したが 0 件」を示すマーカーを検出する。
func isEmptyResultPage(body []byte) bool {
	s := strings.ToLower(string(body[:min(len(body), 32768)]))
	markers := []string{
		"見つかりませんでした", "該当する記事", "no posts",
		"results-empty", "posts-empty", "に一致する記事はありません",
	}
	for _, m := range markers {
		if strings.Contains(s, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// attr は要素の属性値を返す。
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// spanText は n の子孫のうち class=cls を持つ要素のテキストを返す。無ければ n 全体のテキスト。
func spanText(n *html.Node, cls string) string {
	var found *html.Node
	var find func(*html.Node)
	find = func(nd *html.Node) {
		if found != nil {
			return
		}
		if nd.Type == html.ElementNode {
			for _, a := range nd.Attr {
				if a.Key == "class" && strings.Contains(a.Val, cls) {
					found = nd
					return
				}
			}
		}
		for ch := nd.FirstChild; ch != nil; ch = ch.NextSibling {
			find(ch)
		}
	}
	find(n)
	target := found
	if target == nil {
		target = n
	}
	return textContent(target)
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var f func(*html.Node)
	f = func(nd *html.Node) {
		if nd.Type == html.TextNode {
			sb.WriteString(nd.Data)
		}
		for ch := nd.FirstChild; ch != nil; ch = ch.NextSibling {
			f(ch)
		}
	}
	f(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

func absoluteURL(baseURL, href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return baseURL + href
}
