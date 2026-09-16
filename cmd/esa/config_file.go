package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// fileConfig は config.yml の内容。すべて任意項目。
//
// 🚨 `browser` キーは issue 003 で廃止した（Chrome 専用にしたため）。
// loadFileConfig は素の yaml.Unmarshal なので、既存の `browser: Brave` は
// 黙って無視される。さらに saveFileConfig は読み込んだ構造体を書き戻すため、
// `esa config set` / `esa setup` / `esa config init` を一度でも実行すると
// 既存の `browser:` 行はファイルから消える（承知の上）。
type fileConfig struct {
	Profile string `yaml:"profile,omitempty"`
	Team    string `yaml:"team,omitempty"`
}

// configDir は設定ディレクトリ（$XDG_CONFIG_HOME/esa-cli、無ければ ~/.config/esa-cli）を返す。
// cwd には依存しない（HOME / XDG 基準）。
func configDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "esa-cli"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "esa-cli"), nil
}

func configFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yml"), nil
}

var (
	fileConfigOnce   sync.Once
	fileConfigCached fileConfig
	fileConfigErr    error // 解析に失敗したときの理由（config set はこれを見て書き込みを拒む）
)

// fileConfigProblem は config.yml の解析に失敗していればその理由を返す。
func fileConfigProblem() error {
	loadFileConfig()
	return fileConfigErr
}

// loadFileConfig は config.yml を読む（無ければゼロ値）。プロセス内で 1 回だけ読む。
func loadFileConfig() fileConfig {
	fileConfigOnce.Do(func() {
		path, err := configFilePath()
		if err != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return // 無い場合はゼロ値
		}
		var fc fileConfig
		if err := yaml.Unmarshal(data, &fc); err != nil {
			fileConfigErr = fmt.Errorf("%s の解析に失敗しました: %w", path, err)
			fmt.Fprintf(os.Stderr, "警告: %v\n", fileConfigErr)
			return
		}
		fileConfigCached = fc
	})
	return fileConfigCached
}

// saveFileConfig は config.yml を書き出す（ディレクトリごと作成）。
func saveFileConfig(fc fileConfig) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yml")
	data, err := yaml.Marshal(fc)
	if err != nil {
		return err
	}
	header := "# esa-cli 設定ファイル（esa config set で更新できます）\n" +
		"# profile: 使用する Chrome プロファイル名（auto でログイン済みを自動検出）\n" +
		"# team: チーム名（サブドメイン）\n"
	if err := os.WriteFile(path, append([]byte(header), data...), 0o600); err != nil {
		return err
	}
	return nil
}

// resolveDefault は「環境変数 > config.yml > 組み込み既定」の順で既定値を決める。
// これを flag の既定値に使うことで、-flag 明示指定が最優先になる（flag > env > file > 既定）。
func resolveDefault(envKey, fileVal, builtin string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if fileVal != "" {
		return fileVal
	}
	return builtin
}

const configHelp = `esa config - 設定ファイル(config.yml)を表示・編集する

config.yml の場所: $XDG_CONFIG_HOME/esa-cli/config.yml（未設定なら ~/.config/esa-cli/config.yml）
設定できるキー: profile / team
優先順位: コマンドラインフラグ > 環境変数 > config.yml > 組み込み既定

使い方:
  esa config              現在の有効な設定と、その出所（flag/env/file/default）を表示
  esa config path         config.yml のパスを表示
  esa config set <k> <v>  キーを設定して config.yml に保存（例: esa config set profile "Profile 3"）
  esa config get <k>      config.yml のキーの値を表示
  esa config init         ログイン済みプロファイルを自動検出し、profile を config.yml に保存

例:
  esa config set profile "Profile 3"   # 使用プロファイルを固定（自動検出をスキップ）
  esa config init                      # 検出結果を profile に書き込む
  esa config                           # 今の有効設定を確認
`

// configKeys は config.yml に設定できるキー。
// 🚨 `browser` は issue 003 で廃止した。ここへ戻すと fileConfig にフィールドが無いので
// 「保存した」と表示しながら何も書かれない状態になる（config_browser_removed_test.go が守る）。
var configKeys = map[string]bool{"profile": true, "team": true}

func cmdConfig(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "show":
		return configShow()
	case "path":
		p, err := configFilePath()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	case "get":
		if len(args) < 2 {
			return &usageError{"エラー: キー名を指定してください。\n使い方: esa config get <profile|team>"}
		}
		return configGet(args[1])
	case "set":
		// 🚨 読めなかったファイルを「読めたこと」にして上書きしない。
		// 以前は解析に失敗しても警告だけ出してゼロ値から書き直しており、
		// 他の設定（team / profile 等）が黙って消えた。
		if err := fileConfigProblem(); err != nil {
			path, _ := configFilePath()
			return &usageError{fmt.Sprintf(
				"エラー: 設定ファイルを読めないため書き込みを中止しました。\n  %v\n"+
					"  ファイルを直すか削除してから、もう一度実行してください: %s", err, path)}
		}
		if len(args) < 3 {
			return &usageError{"エラー: キーと値を指定してください。\n使い方: esa config set <profile|team> <値>\n例:     esa config set profile \"Profile 3\""}
		}
		return configSet(args[1], args[2])
	case "init":
		return configInit(args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, configHelp)
		return nil
	default:
		return &usageError{fmt.Sprintf("エラー: 不明なサブコマンド %q\n%s", sub, configHelp)}
	}
}

func configShow() error {
	path, _ := configFilePath()
	fc := loadFileConfig()
	exists := false
	if _, err := os.Stat(path); err == nil {
		exists = true
	}

	// 各項目の有効値と出所を求める。
	resolve := func(envKey, fileVal, builtin string) (string, string) {
		if v := os.Getenv(envKey); v != "" {
			return v, "env:" + envKey
		}
		if fileVal != "" {
			return fileVal, "file"
		}
		return builtin, "default"
	}
	team, teamSrc := resolve("ESA_TEAM", fc.Team, "")
	profile, profileSrc := resolve("ESA_CHROME_PROFILE", fc.Profile, "auto")

	fmt.Printf("config file: %s%s\n", path, map[bool]string{true: "", false: "  (未作成)"}[exists])
	fmt.Println("有効な設定（コマンドラインフラグ指定時はそれが最優先）:")
	if team == "" {
		team, teamSrc = "(未設定)", "none"
	}
	fmt.Printf("  %-8s %-14s (%s)\n", "team:", team, teamSrc)
	fmt.Printf("  %-8s %-14s (%s)\n", "profile:", profile, profileSrc)
	if profile == "auto" {
		fmt.Println("\nヒント: プロファイルを固定するなら  esa config set profile \"Profile 3\"  または  esa config init")
	}
	return nil
}

func configGet(key string) error {
	if !configKeys[key] {
		return &usageError{fmt.Sprintf("エラー: 不明なキー %q（指定可能: profile, team）", key)}
	}
	fc := loadFileConfig()
	switch key {
	case "profile":
		fmt.Println(fc.Profile)
	case "team":
		fmt.Println(fc.Team)
	}
	return nil
}

func configSet(key, value string) error {
	if !configKeys[key] {
		return &usageError{fmt.Sprintf("エラー: 不明なキー %q（指定可能: profile, team）", key)}
	}
	fc := loadFileConfig()
	switch key {
	case "profile":
		fc.Profile = value
	case "team":
		fc.Team = value
	}
	if err := saveFileConfig(fc); err != nil {
		return err
	}
	path, _ := configFilePath()
	fmt.Printf("%s に保存しました: %s = %s\n", path, key, value)
	return nil
}

func configInit(args []string) error {
	// -team を受け付ける（どのチームで検出するか）。
	var cfg config
	fs := newFlagSet("config init")
	registerCommon(fs, &cfg)
	// 🚨 素の fs.Parse を呼ばない（setup.go と同じ理由。issue 005）。
	if done, err := parseArgs(fs, configHelp, args, os.Stdout); err != nil || done {
		return err
	}

	// profile を auto にして実際の検出を走らせ、使われるプロファイル名を得る。
	cfg.profile = profileAuto
	name, _, err := resolveProfileClient(cfg)
	if err != nil {
		return err
	}
	fc := loadFileConfig()
	fc.Profile = name
	if fc.Team == "" && cfg.team != "" {
		fc.Team = cfg.team
	}
	if err := saveFileConfig(fc); err != nil {
		return err
	}
	path, _ := configFilePath()
	fmt.Printf("%s に profile = %s を保存しました\n", path, name)
	return nil
}
