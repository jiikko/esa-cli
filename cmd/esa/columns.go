package main

import (
	"fmt"
	"strconv"
	"strings"
)

// colDef は 1 カラムの定義（見出しと値の取り出し）。
type colDef struct {
	header  string
	enrich  bool // 表示に /posts/N.json の enrich が必要か
	extract func(*searchResult) string
}

func dateOnly(iso string) string {
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

// columnRegistry は指定可能なカラム。エイリアスも含む。
var columnRegistry = map[string]colDef{
	"number":     {"番号", false, func(r *searchResult) string { return strconv.Itoa(r.Number) }},
	"name":       {"カテゴリ/タイトル", false, func(r *searchResult) string { return r.FullName }},
	"full_name":  {"カテゴリ/タイトル", false, func(r *searchResult) string { return r.FullName }},
	"title":      {"タイトル", true, func(r *searchResult) string { return r.Title }},
	"category":   {"カテゴリ", true, func(r *searchResult) string { return r.Category }},
	"url":        {"URL", false, func(r *searchResult) string { return r.URL }},
	"created":    {"作成日", true, func(r *searchResult) string { return dateOnly(r.CreatedAt) }},
	"updated":    {"更新日", true, func(r *searchResult) string { return dateOnly(r.UpdatedAt) }},
	"created_at": {"作成日時", true, func(r *searchResult) string { return r.CreatedAt }},
	"updated_at": {"更新日時", true, func(r *searchResult) string { return r.UpdatedAt }},
	"author":     {"author", true, func(r *searchResult) string { return r.UpdatedBy }},
	"updated_by": {"更新者", true, func(r *searchResult) string { return r.UpdatedBy }},
	"created_by": {"作成者", true, func(r *searchResult) string { return r.CreatedBy }},
	"wip": {"wip", true, func(r *searchResult) string {
		if r.Wip == nil {
			return ""
		}
		return strconv.FormatBool(*r.Wip)
	}},
	"tags": {"tags", true, func(r *searchResult) string { return strings.Join(r.Tags, ",") }},
}

// 既定のカラム構成（番号・作成日・更新日・author・カテゴリ/タイトル）。
const defaultColumns = "number,created,updated,author,name"

// parseColumns は "number,updated,author" のような指定を検証して展開する。
func parseColumns(spec string) ([]string, error) {
	if strings.TrimSpace(spec) == "" {
		spec = defaultColumns
	}
	var cols []string
	for _, raw := range strings.Split(spec, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := columnRegistry[name]; !ok {
			return nil, fmt.Errorf("不明なカラム %q。指定可能: %s", name, availableColumns())
		}
		cols = append(cols, name)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("有効なカラムがありません")
	}
	return cols, nil
}

func availableColumns() string {
	// エイリアス（full_name / created_at / updated_at / updated_by）は代表名にまとめて案内。
	return "number, name, title, category, url, created, updated, created_at, updated_at, author, created_by, wip, tags"
}

// columnsNeedEnrich は指定カラムのどれかが enrich を要するか。
func columnsNeedEnrich(cols []string) bool {
	for _, c := range cols {
		if columnRegistry[c].enrich {
			return true
		}
	}
	return false
}

// renderTable はタブ区切りでヘッダ + 各行を出力する（全角/半角の桁揃えは避け、タブに委ねる）。
func renderTable(results []searchResult, cols []string, header bool) string {
	var sb strings.Builder
	if header {
		hs := make([]string, len(cols))
		for i, c := range cols {
			hs[i] = columnRegistry[c].header
		}
		sb.WriteString(strings.Join(hs, "\t"))
		sb.WriteByte('\n')
	}
	for i := range results {
		vs := make([]string, len(cols))
		for j, c := range cols {
			vs[j] = columnRegistry[c].extract(&results[i])
		}
		sb.WriteString(strings.Join(vs, "\t"))
		sb.WriteByte('\n')
	}
	return sb.String()
}
