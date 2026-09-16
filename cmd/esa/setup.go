package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const setupHelp = `esa setup - 対話式セットアップウィザード

team・使用する Chrome プロファイルを順に尋ね、認証確認のうえ config.yml に保存する。
プロファイルは候補ごとに「ログイン中メール」と「esa 認証が通るか」を表示して選べる。

使い方:
  esa setup            対話的に設定して保存
  esa setup -team X    既定値を渡して開始（プロンプトで上書き可）

非対話（パイプ/入力なし）で実行した場合は各項目とも既定値を採用する。
team に既定が無い（未設定）まま非対話だと、team 必須エラーで終了する。
`

// promptDefault は 1 行入力を求める。空入力/EOF なら def を返す。
func promptDefault(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		if err != nil { // EOF（非対話）
			fmt.Println()
		}
		return def
	}
	return line
}

func cmdSetup(args []string) error {
	var cfg config
	fs := newFlagSet("setup", setupHelp)
	registerCommon(fs, &cfg) // 現在の既定（env/config.yml）を初期値として使う
	fs.Parse(args)

	in := bufio.NewReader(os.Stdin)
	fmt.Println("=== esa セットアップ ===")
	fmt.Println("esa (esa.io) の任意チームを対象にできます。以下を設定します。")
	fmt.Println()

	// 1. team
	team := strings.TrimSpace(promptDefault(in, "チーム名（https://<team>.esa.io の <team>）", cfg.team))
	if team == "" {
		return &usageError{"エラー: team は必須です。もう一度 esa setup を実行し、チーム名を入力してください。"}
	}
	cfg.team = team

	// 2. プロファイル検出（esa Cookie を持つものを列挙し、認証可否とメールを表示）
	fmt.Printf("\n%s のプロファイルを調べています（%s の認証を確認）...\n", chromeName, cfg.teamHost())
	type cand struct {
		dir, email string
		authed     bool
	}
	var cands []cand
	firstAuthed := -1
	for _, pi := range listProfileInfos() {
		c, err := buildClientForProfile(cfg, pi.dir)
		if err != nil {
			continue // esa Cookie が無いプロファイルは候補外
		}
		authed := c.authOK()
		if authed && firstAuthed < 0 {
			firstAuthed = len(cands)
		}
		cands = append(cands, cand{dir: pi.dir, email: pi.email, authed: authed})
	}
	if len(cands) == 0 {
		return fmt.Errorf(
			"%s に esa（%s）の Cookie を持つプロファイルが見つかりませんでした。\n"+
				"  %s で https://%s にログインしてから、もう一度 esa setup を実行してください。",
			chromeName, cfg.teamHost(), chromeName, cfg.teamHost())
	}

	fmt.Println("\n候補プロファイル:")
	for i, c := range cands {
		mark := "—（このチームでは未認証）"
		if c.authed {
			mark = "✓ 認証OK"
		}
		email := c.email
		if email == "" {
			email = "-"
		}
		fmt.Printf("  [%d] %-12s  %-38s  %s\n", i+1, c.dir, email, mark)
	}
	if firstAuthed < 0 {
		fmt.Println("\n注意: どのプロファイルも現在このチームで認証が通っていません（セッション切れの可能性）。")
		fmt.Printf("      %s で https://%s にログインし直すと確実です。\n", chromeName, cfg.teamHost())
	}

	def := ""
	if firstAuthed >= 0 {
		def = strconv.Itoa(firstAuthed + 1) // 認証OKの最初を既定に
	}
	sel := strings.TrimSpace(promptDefault(in, "使用するプロファイルの番号", def))
	idx, err := strconv.Atoi(sel)
	if err != nil || idx < 1 || idx > len(cands) {
		return &usageError{fmt.Sprintf("エラー: 番号 %q が不正です（1〜%d を指定）", sel, len(cands))}
	}
	chosen := cands[idx-1]
	if !chosen.authed {
		fmt.Printf("警告: プロファイル %q は今このチームで認証が通りませんが、指定どおり保存します。\n", chosen.dir)
	}
	cfg.profile = chosen.dir

	// 3. 保存
	fc := loadFileConfig()
	fc.Team = cfg.team
	fc.Profile = cfg.profile
	if err := saveFileConfig(fc); err != nil {
		return err
	}
	path, _ := configFilePath()

	fmt.Printf("\n保存しました: %s\n", path)
	fmt.Printf("  team=%s  profile=%s\n", cfg.team, cfg.profile)
	fmt.Println("\n準備完了。次のように使えます:")
	fmt.Println("  esa search 'キーワード'")
	fmt.Println("  esa show <番号>")
	return nil
}
