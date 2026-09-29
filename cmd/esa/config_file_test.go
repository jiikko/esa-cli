package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// resetFileConfigCache は loadFileConfig の 1 回だけの読み込みをやり直せるようにする。
func resetFileConfigCache(t *testing.T) {
	t.Helper()
	fileConfigOnce = sync.Once{}
	fileConfigCached, fileConfigErr = fileConfig{}, nil
	t.Cleanup(func() {
		fileConfigOnce = sync.Once{}
		fileConfigCached, fileConfigErr = fileConfig{}, nil
	})
}

// config get / set の --help は、ヘルプを stdout へ出して rc=0 にし、何も保存しないこと（issue 009）。
// 以前は get では「不明なキー "--help"」、set profile --help では profile に "--help" を保存していた。
// cmdConfig は TestSubcommandsGoThroughParseArgs の対象から外れているので、ここで直接固定する。
func TestConfigGetSetHelp(t *testing.T) {
	for _, args := range [][]string{
		{"get", "--help"}, {"get", "-h"}, {"get", "-help"}, {"get", "--h"}, {"get", "help"},
		{"set", "--help"}, {"set", "-h"}, {"set", "-help"}, {"set", "--h"}, {"set", "help"},
		{"set", "profile", "--help"}, {"set", "team", "-h"},
	} {
		cfg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", cfg)
		resetFileConfigCache(t)
		var err error
		stdout, stderr := captureStdio(t, func() { err = cmdConfig(args) })
		if err != nil || stdout != configHelp || stderr != "" {
			t.Errorf("esa config %v: ヘルプが stdout に出ていない（err=%v stderr=%q）", args, err, stderr)
		}
		if _, err := os.Stat(filepath.Join(cfg, "esa-cli", "config.yml")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("esa config %v: ヘルプを求められたのに config.yml を書いた: %v", args, err)
		}
	}
}

// 設定ファイルが壊れていても set --help はヘルプを出し、ファイルを書き換えないこと。
func TestConfigSetHelpWithBrokenConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	resetFileConfigCache(t)
	path := filepath.Join(cfg, "esa-cli", "config.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	broken := "team: [\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	stdout, _ := captureStdio(t, func() { err = cmdConfig([]string{"set", "--help"}) })
	if err != nil || stdout != configHelp {
		t.Errorf("壊れた config.yml のとき set --help がヘルプを出さない: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != broken {
		t.Errorf("config.yml を書き換えた: %q", b)
	}
}

// 値の位置の help は team 名として正当（help.esa.io）なので、ヘルプにせず保存すること。
func TestConfigSetTeamNamedHelp(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	resetFileConfigCache(t)
	var err error
	captureStdio(t, func() { err = cmdConfig([]string{"set", "team", "help"}) })
	if err != nil {
		t.Fatalf("team=help を保存できない: %v", err)
	}
	resetFileConfigCache(t)
	if got := loadFileConfig().Team; got != "help" {
		t.Errorf("team が保存されていない: %q", got)
	}
}
