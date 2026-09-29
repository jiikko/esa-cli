package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// config.yml を esa config set と esa sync add の両方が書いても、互いのキーと手で書いたコメントを消さないこと（issue 011）。
func TestConfigSetKeepsSyncTargetsAndComments(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	resetFileConfigCache(t)
	path := filepath.Join(cfg, "esa-cli", "config.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	hand := "# 手で書いたコメント\nprofile: Default # 行末のコメント\nsync:\n  - name: skills # 対象のコメント\n    category: Users/me/skills\n    dir: ~/.claude/skills\nlegacy: keep-me\n"
	if err := os.WriteFile(path, []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdio(t, func() { err = configSet("team", "myteam") })
	if err != nil {
		t.Fatalf("config set team: %v", err)
	}
	out := readFile(t, path)
	for _, want := range []string{"# 手で書いたコメント", "# 行末のコメント", "# 対象のコメント", "team: myteam", "profile: Default", "legacy: keep-me"} {
		if !strings.Contains(out, want) {
			t.Errorf("config set の後に %q が無い:\n%s", want, out)
		}
	}
	targets, _, err := loadSyncTargets()
	if err != nil || len(targets) != 1 || targets[0].Name != "skills" {
		t.Fatalf("config set の後に sync の対象が読めない: %+v %v\n%s", targets, err, out)
	}

	// 逆向き: sync add の追記が profile / team を消さない
	if err := appendSyncTarget(path, syncTarget{Name: "b", Category: "D", Dir: "/y"}); err != nil {
		t.Fatalf("追記できない: %v", err)
	}
	resetFileConfigCache(t)
	if fc := loadFileConfig(); fc.Team != "myteam" || fc.Profile != "Default" {
		t.Errorf("sync add の後に profile / team が変わった: %+v\n%s", fc, readFile(t, path))
	}
	if targets, _, err := loadSyncTargets(); err != nil || len(targets) != 2 {
		t.Errorf("追記の後の対象: %+v %v", targets, err)
	}
}

// 空の値はキーごと消し（以前の omitempty と同じ）、sync: は残すこと。
func TestSaveFileConfigRemovesEmptyKeys(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	resetFileConfigCache(t)
	path := filepath.Join(cfg, "esa-cli", "config.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("profile: Default\nteam: t\nsync:\n  - {name: a, category: C, dir: /x}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveFileConfig(fileConfig{Team: "t"}); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	if strings.Contains(out, "profile") || !strings.Contains(out, "team: t") || !strings.Contains(out, "name: a") {
		t.Errorf("空の profile が消えていない、または他が消えた:\n%s", out)
	}
}

// 新しい config.yml はヘッダ付きで 0600 で作ること。
func TestSaveFileConfigCreatesWithHeader(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	resetFileConfigCache(t)
	if err := saveFileConfig(fileConfig{Team: "t", Profile: "Default"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg, "esa-cli", "config.yml")
	out := readFile(t, path)
	if !strings.HasPrefix(out, "# esa-cli 設定ファイル") || !strings.Contains(out, "team: t") || !strings.Contains(out, "profile: Default") {
		t.Errorf("新規作成の内容が違う:\n%s", out)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("権限が 0600 でない: %v", fi.Mode().Perm())
	}
}

// 読めない config.yml（壊れた YAML・複数の文書・最上位がマッピングでない）には、profile / team の保存でも書かないこと。
// 以前は setup / config init が struct から書き直していたので、中身を消して上書きしていた。
func TestSaveFileConfigRefusesUnreadableFile(t *testing.T) {
	for name, body := range map[string]string{
		"壊れた YAML": "team: [\n",
		"複数の文書":    "team: a\n---\nsync:\n  - {name: a, category: C, dir: /x}\n",
		"最上位がリスト":  "- a\n- b\n",
		"最上位がスカラー": "hello\n",
	} {
		cfg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", cfg)
		resetFileConfigCache(t)
		path := filepath.Join(cfg, "esa-cli", "config.yml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := saveFileConfig(fileConfig{Team: "t"}); err == nil {
			t.Errorf("%s: 保存がエラーにならなかった", name)
		}
		if got := readFile(t, path); got != body {
			t.Errorf("%s: 書き換えた:\n%s", name, got)
		}
	}
}

// 以前の sync.yml が残っていたら、読まずに移すよう案内してエラーにすること（両方を読むと正本が分からなくなる）。
func TestLegacySyncYAMLIsRefused(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := filepath.Join(cfg, "esa-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sync.yml"), []byte("targets:\n  - {name: a, category: C, dir: /x}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadSyncTargets()
	if err == nil || !strings.Contains(err.Error(), "sync.yml は使わなくなりました") || !strings.Contains(err.Error(), "sync:") {
		t.Errorf("sync.yml が残っているのにエラーにならない / 案内が無い: %v", err)
	}
}
