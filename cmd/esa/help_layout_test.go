package main

import (
	"strings"
	"testing"
)

// root の help は概要とサブコマンドの一覧だけにし、詳細（共通オプション・終了コード・認証）は各サブコマンドの help に置くこと。
// root に詳細を書き足すと、サブコマンドの help と二重になって片方だけ直る。
func TestTopUsageIsSummaryOnly(t *testing.T) {
	for _, sub := range []string{"search", "show", "meta", "revisions", "sync", "config", "setup", "help"} {
		if !strings.Contains(topUsage, "\n  "+sub+" ") {
			t.Errorf("root の help にサブコマンド %q が無い", sub)
		}
	}
	for _, detail := range []string{"終了コード:", "-team <name>", "-profile <name>", "Keychain", "優先順位:"} {
		if strings.Contains(topUsage, detail) {
			t.Errorf("root の help に詳細 %q がある（各サブコマンドの help に置く）", detail)
		}
	}
	if n := strings.Count(topUsage, "\n"); n > 20 {
		t.Errorf("root の help が %d 行ある（概要だけにする）", n)
	}
}

// esa に問い合わせるサブコマンドの help は、それだけで共通オプション・終了コード・認証が分かること（root を参照させない）。
func TestSubcommandHelpsCarryCommonDetails(t *testing.T) {
	for name, h := range map[string]string{
		"search": searchHelp, "show": showHelp, "meta": metaHelp, "revisions": revisionsHelp,
		"sync": syncHelp, "sync add": syncAddHelp, "setup": setupHelp,
	} {
		for _, want := range []string{"-team <name>", "-profile <name>", "終了コード:", "認証:"} {
			if !strings.Contains(h, want) {
				t.Errorf("esa %s --help に %q が無い", name, want)
			}
		}
		if strings.Contains(h, "esa --help を参照") {
			t.Errorf("esa %s --help が root の help を参照している", name)
		}
	}
	if !strings.Contains(syncListHelp, "esa sync list") || strings.Contains(syncListHelp, "--apply") {
		t.Error("esa sync list --help が list 専用の help になっていない")
	}
}
