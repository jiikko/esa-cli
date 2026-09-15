package main

import (
	"strings"
	"testing"
)

// 0 件ページと「構造が変わった」ページを区別できること。
//
// 🚨 fixture は実際の esa の HTML を貼らず、判定に必要な構造だけを再現する
// （社内の記事名・チーム名を公開リポジトリに持ち込まないため）。
// 判定に使う class 名は実レスポンスから確認した（2026-09-16）。
//
// 0 件マーカーは実際のページで 51,741 バイト目に在った。以前の実装は先頭 32,768 バイト
// しか見ておらず、**本当に 0 件のときに「抽出できませんでした」**と報告していた。
// その退行を再現するため、fixture はマーカーの前に 40KB の詰め物を置く。
func TestEmptyResultPageDetection(t *testing.T) {
	filler := strings.Repeat("<div class=\"sidebar-item\">メニュー</div>\n", 1000) // 約 40KB

	emptyPage := `<html><body>` + filler + `
<div class="layout-search__main">
  <div class="layout-search__content">
    <div class="search__no-result">
      <p class="search__no-result-message"><strong>"q"</strong>の検索結果は見つかりませんでした</p>
    </div>
  </div>
</div></body></html>`

	brokenPage := `<html><body>` + filler + `
<div class="layout-search__main">
  <div class="layout-search__content">
    <div class="totally-different-markup">なにか</div>
  </div>
</div></body></html>`

	if !isEmptyResultPage([]byte(emptyPage)) {
		t.Error("0 件ページを検出できていない（先頭 N バイトしか見ていない実装だとここで落ちる）")
	}
	if isEmptyResultPage([]byte(brokenPage)) {
		t.Error("0 件マーカーが無いページを 0 件と誤判定している")
	}
	if isEmptyResultPage([]byte("<html><body>壊れていない普通のページ</body></html>")) {
		t.Error("無関係なページを 0 件と誤判定している")
	}
}

// searchViaHTML の分岐（0 件 / 抽出失敗）は parseSearchHTML と isEmptyResultPage の
// 組み合わせで決まる。ヒットありのページでは 0 件判定に落ちないこと。
func TestParseSearchHTMLFindsPosts(t *testing.T) {
	page := `<html><body>
<a class="post-title__link" href="/posts/12345"><span class="post-title__name">カテゴリ/タイトル</span></a>
<a class="post-title__link" href="/posts/12346"><span class="post-title__name">別のタイトル</span></a>
</body></html>`

	results, err := parseSearchHTML([]byte(page), "https://example.esa.io")
	if err != nil {
		t.Fatalf("parseSearchHTML: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("記事数: got %d, want 2", len(results))
	}
	if results[0].Number != 12345 || results[0].Title != "カテゴリ/タイトル" {
		t.Errorf("1 件目が違う: %+v", results[0])
	}
	if results[0].URL != "https://example.esa.io/posts/12345" {
		t.Errorf("URL が絶対化されていない: %q", results[0].URL)
	}
	if isEmptyResultPage([]byte(page)) {
		t.Error("ヒットのあるページを 0 件と判定している")
	}
}
