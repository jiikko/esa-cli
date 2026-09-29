package main

import (
	"reflect"
	"strings"
	"testing"
)

// -columns の解釈: 空は既定、空要素と前後の空白は無視、不明なカラムは指定可能な一覧つきで拒否する。
func TestParseColumns(t *testing.T) {
	got, err := parseColumns("")
	if err != nil || strings.Join(got, ",") != defaultColumns {
		t.Errorf("空は既定（%s）になるべき: %v %v", defaultColumns, got, err)
	}
	got, err = parseColumns(" number , ,title,")
	if err != nil || !reflect.DeepEqual(got, []string{"number", "title"}) {
		t.Errorf("空要素と空白を無視していない: %v %v", got, err)
	}
	if _, err := parseColumns("number,nosuch"); err == nil || !strings.Contains(err.Error(), `"nosuch"`) || !strings.Contains(err.Error(), "指定可能") {
		t.Errorf("不明なカラムを名前と候補つきで拒否していない: %v", err)
	}
	if _, err := parseColumns(" , "); err == nil {
		t.Error("有効なカラムが 1 つも無いのに成功した")
	}
}

// enrich（記事ごとの JSON 取得）が要るのは、検索 HTML に無いカラムを 1 つでも含むときだけ。
// 要らないのに取ると記事数ぶん通信が増え、要るのに取らないと列が空になる。
func TestColumnsNeedEnrich(t *testing.T) {
	if columnsNeedEnrich([]string{"number", "title", "url"}) {
		t.Error("検索 HTML だけで足りるカラムなのに enrich する")
	}
	for _, c := range []string{"category", "updated", "author", "tags"} {
		if !columnsNeedEnrich([]string{"number", c}) {
			t.Errorf("%s を含むのに enrich しない（列が空になる）", c)
		}
	}
}

func TestDateOnly(t *testing.T) {
	for in, want := range map[string]string{
		"2026-09-28T10:13:30+09:00": "2026-09-28",
		"2026-09-28":                "2026-09-28",
		"short":                     "short",
		"":                          "",
	} {
		if got := dateOnly(in); got != want {
			t.Errorf("dateOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// 記事 JSON で検索結果を補う: esa のエスケープ（&#47; / &#35;）を復元し、無い項目は触らない。
func TestApplyPostJSON(t *testing.T) {
	r := searchResult{Number: 1, URL: "https://t.esa.io/posts/1", Tags: []string{"old"}}
	applyPostJSON(&r, map[string]any{
		"full_name":  "dev&#47;Tips&#47;C&#35; の話",
		"name":       "C&#35; の話",
		"category":   "dev&#47;Tips",
		"created_at": "2026-09-01T00:00:00+09:00",
		"updated_at": "2026-09-02T00:00:00+09:00",
		"wip":        false,
		"created_by": map[string]any{"screen_name": "alice"},
		"updated_by": map[string]any{"screen_name": "bob"},
		"tags":       []any{"go", "tui"},
	})
	if r.FullName != "dev/Tips/C# の話" || r.Title != "C# の話" || r.Category != "dev/Tips" {
		t.Errorf("エスケープを復元していない: %+v", r)
	}
	if r.CreatedBy != "alice" || r.UpdatedBy != "bob" || r.Wip == nil || *r.Wip {
		t.Errorf("作成者・更新者・wip の取り込み: %+v", r)
	}
	if !reflect.DeepEqual(r.Tags, []string{"go", "tui"}) {
		t.Errorf("タグは置き換えるべき（前の値を残さない）: %v", r.Tags)
	}
	if r.Number != 1 || r.URL != "https://t.esa.io/posts/1" {
		t.Errorf("JSON に無い項目を壊した: %+v", r)
	}

	keep := searchResult{Title: "元のタイトル"}
	applyPostJSON(&keep, map[string]any{"name": ""})
	if keep.Title != "元のタイトル" {
		t.Errorf("空の name で既存のタイトルを消した: %q", keep.Title)
	}
}

// -c の誤りは使い方の誤り（rc=2）として返すこと（README の終了コードの契約。issue 008）。
func TestParseColumnsErrorsAreUsageErrors(t *testing.T) {
	for _, spec := range []string{"number,nosuch", " , "} {
		_, err := parseColumns(spec)
		if code := exitCodeFor(err); code != 2 {
			t.Errorf("parseColumns(%q) の終了コードが %d（2 のはず）: %v", spec, code, err)
		}
		if err == nil || !strings.HasPrefix(err.Error(), "エラー: ") || !strings.Contains(err.Error(), "esa search --help") {
			t.Errorf("parseColumns(%q) のメッセージが他の使い方エラーの書式と違う: %v", spec, err)
		}
	}
	// 検索の本体まで通しても 2 になり、esa へ問い合わせる前に止まること（team を指定しないので、
	// カラムの検査より先に team の検査が走れば別のエラーになる）
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetFileConfigCache(t) // 他のテストが読んだ config.yml のキャッシュを使わない
	t.Setenv("ESA_TEAM", "")
	var err error
	captureStdio(t, func() { err = cmdSearch([]string{"-c", "nosuch", "q"}) })
	if exitCodeFor(err) != 2 || !strings.Contains(err.Error(), "不明なカラム") {
		t.Errorf("esa search -c nosuch が rc=2 の不明なカラムにならない: %v", err)
	}
}
