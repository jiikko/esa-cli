package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"os"
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
		if failed, firstErr := c.enrichResults(results, fetchConcurrency); failed > 0 {
			// 基本情報（number/title/url）のみで残す方針は維持する。rc は 0 のまま。
			// ただし黙って空欄にしない（日付・author が空なのが取得失敗だと分かるように）。
			fmt.Fprintf(os.Stderr, "警告: %d/%d 件の詳細取得に失敗しました（該当行は number/title/url のみ）。最初のエラー: %v\n",
				failed, len(results), firstErr)
		}
	}
	return results, nil
}

// esaAPIBase は公式 API の起点。テストでは httptest サーバへ差し替える。
var esaAPIBase = "https://api.esa.io"

// searchViaAPI は api.esa.io の公式 API を使う（ESA_TOKEN 指定時のみ）。詳細まで一括で返る。
func searchViaAPI(teamName, query string, perPage, page int, token string) ([]searchResult, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	api := fmt.Sprintf("%s/v1/teams/%s/posts?%s", esaAPIBase, teamName, q.Encode())

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
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("公式 API: %w", rateLimitError(api))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("公式 API: 予期しないステータス %d", resp.StatusCode)
	}
	body, err := readLimitedBody(resp.Body, api) // get() と同じ上限（client.go）
	if err != nil {
		return nil, fmt.Errorf("公式 API: %w", err)
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
	if err := json.Unmarshal(body, &out); err != nil {
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
	results, _, err := c.searchPage(query, page)
	return results, err
}

// searchPage は検索結果の 1 ページを取得し、次のページがあるかも返す。
//
// 🚨 次のページの有無は pagination の rel="next" のリンクで判定する。件数や空ページでは判定できない
// （実測 2026-09-29）: sort 指定なしで最後より先のページを要求すると 1 ページ目がもう一度返り、
// sort 指定ありだと記事 0 件で「0 件」の目印も無いページが返る（後者は errParseFailed になる）。
// どちらの場合も、最後のページには rel="next" が無い。
func (c *client) searchPage(query string, page int) (results []searchResult, hasNext bool, err error) {
	q := url.Values{}
	q.Set("q", query)
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	body, ct, err := c.get("/posts?" + q.Encode())
	if err != nil {
		return nil, false, err
	}
	if !strings.Contains(ct, "html") {
		return nil, false, fmt.Errorf("検索: HTML 以外が返りました（Content-Type=%q）", ct)
	}

	results, err = parseSearchHTML(body, c.baseURL)
	if err != nil {
		return nil, false, err
	}
	if len(results) == 0 {
		if isEmptyResultPage(body) {
			return []searchResult{}, false, nil // 明示的な 0 件
		}
		return nil, false, errParseFailed // 抽出失敗（セレクタ変更の疑い）
	}
	return results, hasNextPageLink(body), nil
}

// hasNextPageLink は検索結果の pagination に rel="next" のリンクがあるかを返す。
// <head> の <link rel="next"> と取り違えないよう、class="pagination" の要素の中の <a> だけを見る。
func hasNextPageLink(body []byte) bool {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return false
	}
	var inPagination func(*html.Node, bool) bool
	inPagination = func(n *html.Node, inside bool) bool {
		if n.Type == html.ElementNode {
			if !inside {
				for _, cls := range strings.Fields(attr(n, "class")) {
					if cls == "pagination" {
						inside = true
					}
				}
			}
			if inside && n.Data == "a" {
				for _, rel := range strings.Fields(attr(n, "rel")) {
					if rel == "next" {
						return true
					}
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if inPagination(ch, inside) {
				return true
			}
		}
		return false
	}
	return inPagination(doc, false)
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
// 取得に失敗した記事は基本情報のみで残し、失敗件数と最初に記録されたエラーを返す。
func (c *client) enrichResults(results []searchResult, concurrency int) (failed int, firstErr error) {
	if len(results) == 0 {
		return 0, nil
	}
	return forEachConcurrent(len(results), concurrency, func(i int) error {
		post, err := c.postJSON(results[i].Number, false)
		if err != nil {
			return fmt.Errorf("記事 %d: %w", results[i].Number, err)
		}
		applyPostJSON(&results[i], post)
		return nil
	})
}

// forEachConcurrent は fn(0..n-1) を最大 concurrency 並列で実行し、失敗件数と最初に記録された
// エラーを返す。fn は自分の添字の要素だけを書き換えること（それ以外の共有状態は守らない）。
func forEachConcurrent(n, concurrency int, fn func(i int) error) (failed int, firstErr error) {
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex // failed / firstErr を守る
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(i); err != nil {
				mu.Lock()
				failed++
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return failed, firstErr
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
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return false
	}
	return hasClassPrefix(doc, "search__no-result")
}

// hasClassPrefix は class 属性が prefix で始まる要素が在るかを返す。
//
// 🚨 文字列の先頭 N バイトを見る形にしないこと。実測 2026-09-16: 0 件ページの
// マーカーは 51,741 バイト目に在り、以前の実装（先頭 32,768 バイトを検索）の窓の
// 外だった。そのため**本当に 0 件のときに「抽出できませんでした」**と報告していた。
// ページの長さは記事数・サイドバー・チーム設定で変わるので、窓では判定できない。
//
// class は "search__no-result-message" のような派生もあるので前方一致で見る。
func hasClassPrefix(n *html.Node, prefix string) bool {
	if n.Type == html.ElementNode {
		for _, cls := range strings.Fields(attr(n, "class")) {
			if strings.HasPrefix(cls, prefix) {
				return true
			}
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if hasClassPrefix(ch, prefix) {
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
