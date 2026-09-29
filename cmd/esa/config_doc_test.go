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

// setupConfig は一時の XDG_CONFIG_HOME に config.yml を書き、そのパスを返す。
func setupConfig(t *testing.T, body string, perm os.FileMode) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	resetFileConfigCache(t)
	path := filepath.Join(cfg, "esa-cli", "config.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	return path
}

// issue 011 の red team が再現した形の回帰テスト。
func TestConfigDocRedTeamRegressions(t *testing.T) {
	t.Run("空の profile を持つファイルで config set しても、先頭のコメントと空の profile を消さない", func(t *testing.T) {
		path := setupConfig(t, "# my header\nprofile:\nteam: foo\n", 0o600)
		var err error
		captureStdio(t, func() { err = configSet("team", "bar") })
		if err != nil {
			t.Fatal(err)
		}
		out := readFile(t, path)
		if !strings.Contains(out, "# my header") || !strings.Contains(out, "profile:") || !strings.Contains(out, "team: bar") {
			t.Errorf("コメントか空の profile が消えた:\n%s", out)
		}
	})
	t.Run("config set profile \"\" で消すキーの上のコメントは次のキーへ移す", func(t *testing.T) {
		path := setupConfig(t, "# my header\n# line2\nprofile: X\nteam: foo\n", 0o600)
		if err := saveFileConfig(fileConfig{Team: "foo"}); err != nil {
			t.Fatal(err)
		}
		out := readFile(t, path)
		if strings.Contains(out, "profile") || !strings.Contains(out, "# my header") || !strings.Contains(out, "# line2") {
			t.Errorf("コメントが消えたか profile が残った:\n%s", out)
		}
	})
	t.Run("最後のキーを消しても末尾のコメントを残す", func(t *testing.T) {
		path := setupConfig(t, "team: foo\nprofile: X\n# trailing comment\n", 0o600)
		if err := saveFileConfig(fileConfig{Team: "foo"}); err != nil {
			t.Fatal(err)
		}
		if out := readFile(t, path); !strings.Contains(out, "# trailing comment") || strings.Contains(out, "profile") {
			t.Errorf("末尾のコメントが消えた:\n%s", out)
		}
	})
	t.Run("重複した sync: には追記せず、読み込みもエラーにする", func(t *testing.T) {
		body := "team: foo\nsync:\n  - {name: a, category: C, dir: /x}\nsync:\n  - {name: b, category: D, dir: /y}\n"
		path := setupConfig(t, body, 0o600)
		if err := appendSyncTarget(path, syncTarget{Name: "n", Category: "E", Dir: "/z"}); err == nil {
			t.Error("重複した sync: のファイルに追記した")
		}
		if got := readFile(t, path); got != body {
			t.Errorf("書き換えた:\n%s", got)
		}
		if _, _, err := loadSyncTargets(); err == nil {
			t.Error("重複した sync: を黙って読んだ")
		}
	})
	t.Run("team: [a] のように profile / team として読めないファイルには sync add も書かない", func(t *testing.T) {
		body := "team: [a]\n"
		path := setupConfig(t, body, 0o600)
		if err := appendSyncTarget(path, syncTarget{Name: "n", Category: "E", Dir: "/z"}); err == nil {
			t.Error("読めないファイルに追記した")
		}
		if got := readFile(t, path); got != body {
			t.Errorf("書き換えた:\n%s", got)
		}
	})
	t.Run("sync.yml の中身を貼った targets: は案内してエラーにする", func(t *testing.T) {
		setupConfig(t, "team: foo\ntargets:\n  - {name: a, category: C, dir: /x}\n", 0o600)
		if _, _, err := loadSyncTargets(); err == nil || !strings.Contains(err.Error(), "targets: を sync: に書き換えて") {
			t.Errorf("targets: を黙って読んだ / 案内が無い: %v", err)
		}
	})
	t.Run("BOM 付きのコメントだけのファイルでも、利用者のコメントを残す", func(t *testing.T) {
		path := setupConfig(t, "\xef\xbb\xbf# my comments\n# more\n", 0o600)
		var err error
		captureStdio(t, func() { err = configSet("team", "foo") })
		if err != nil {
			t.Fatal(err)
		}
		if out := readFile(t, path); !strings.Contains(out, "# my comments") || strings.Contains(out, "esa-cli 設定ファイル") {
			t.Errorf("利用者のコメントが既定のヘッダに置き換わった:\n%s", out)
		}
	})
	t.Run("アンカーを持つ team は、profile の保存で壊さない", func(t *testing.T) {
		path := setupConfig(t, "team: &t foo\nother: *t\n", 0o600)
		var err error
		captureStdio(t, func() { err = configSet("profile", "P") })
		if err != nil {
			t.Fatalf("アンカーのある設定で config set が失敗した: %v", err)
		}
		if out := readFile(t, path); !strings.Contains(out, "&t") || !strings.Contains(out, "profile: P") {
			t.Errorf("アンカーが壊れた:\n%s", out)
		}
	})
	t.Run("既存のファイルの権限を保つ", func(t *testing.T) {
		path := setupConfig(t, "team: foo\n", 0o644)
		if err := saveFileConfig(fileConfig{Team: "bar"}); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
			t.Errorf("権限が変わった: %v", fi.Mode().Perm())
		}
	})
	t.Run("<<: のマージで入っている profile は消せず、読み戻しの検査でエラーにし、書き換えない", func(t *testing.T) {
		body := "base: &b {profile: X}\n<<: *b\nteam: t\n"
		path := setupConfig(t, body, 0o600)
		err := saveFileConfig(fileConfig{Team: "t"})
		if err == nil || !strings.Contains(err.Error(), "読み戻すと") {
			t.Errorf("読み戻しの検査でエラーにならない: %v", err)
		}
		if got := readFile(t, path); got != body {
			t.Errorf("書き換えた:\n%s", got)
		}
	})
}
