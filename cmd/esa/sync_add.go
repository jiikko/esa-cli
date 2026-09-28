package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

const syncAddHelp = `esa sync add - esa sync の対象を対話式で追加する

esa のカテゴリ・書き出し先のディレクトリ・名前を順に尋ね、sync.yml に追記する。
保存の前に、カテゴリ配下の記事がいくつ見つかるかを esa に問い合わせて表示する。

使い方:
  esa sync add                         対話的に追加
  esa sync add -category C -dir D      既定値を渡して開始（プロンプトで上書き可）

オプション:
  -category <カテゴリ>  esa のカテゴリ。カテゴリを開いたときの https://<team>.esa.io/#path=... の URL を
                        そのまま貼ってもよい（記事の URL は不可。URL のチームは設定中の team と同じであること）
  -dir <ディレクトリ>   書き出し先（絶対パスか ~ 始まり）
  -name <名前>          esa sync <名前> で指定する名前（既定: カテゴリの末尾）
  （共通オプション -team/-profile は esa --help を参照）

非対話（パイプ/入力なし）で実行した場合は各項目とも既定値を採用する。
既定の無い項目が空のままだと、使い方エラーで終了する。

例:
  esa sync add
  esa sync add -category 'Users/me/skills' -dir ~/.claude/skills </dev/null
`

func syncAdd(args []string) error {
	var cfg config
	var t syncTarget
	fs := newFlagSet("sync add")
	registerCommon(fs, &cfg)
	fs.StringVar(&t.Category, "category", "", "esa のカテゴリ（URL も可）")
	fs.StringVar(&t.Dir, "dir", "", "書き出し先（絶対パスか ~ 始まり）")
	fs.StringVar(&t.Name, "name", "", "esa sync <名前> で指定する名前")
	positional, done, err := parsePositionals(fs, syncAddHelp, args)
	if err != nil || done {
		return err
	}
	if len(positional) == 1 && positional[0] == "help" { // esa config help と揃える
		fmt.Fprint(os.Stdout, syncAddHelp)
		return nil
	}
	if len(positional) > 0 {
		return &usageError{fmt.Sprintf("エラー: 余分な引数があります: %s\n詳細:   esa sync add --help", strings.Join(positional, " "))}
	}
	targets, path, err := loadSyncTargets()
	if err != nil {
		return err // 読めない sync.yml に追記しない
	}

	in := bufio.NewReader(os.Stdin)
	fmt.Println("=== esa sync の対象を追加 ===")
	fmt.Println("esa のカテゴリ配下の記事を、ローカルのディレクトリへ書き出す対象を登録します。")
	fmt.Println()

	def, err := parseCategoryInput(t.Category, cfg.team)
	if err != nil {
		return err
	}
	if t.Category, err = parseCategoryInput(promptDefault(in, "esa のカテゴリ（URL を貼ってもよい）", def), cfg.team); err != nil {
		return err
	}
	if t.Category == "" {
		return &usageError{"エラー: カテゴリは必須です。\n詳細:   esa sync add --help"}
	}
	t.Dir = strings.TrimSpace(promptDefault(in, "書き出し先のディレクトリ（絶対パスか ~ 始まり）", t.Dir))
	if t.Dir == "" {
		return &usageError{"エラー: 書き出し先のディレクトリは必須です。\n詳細:   esa sync add --help"}
	}
	if _, err := expandDir(t.Dir); err != nil {
		return &usageError{"エラー: " + err.Error()}
	}
	if t.Name == "" {
		t.Name = defaultSyncName(t.Category)
	}
	t.Name = strings.TrimSpace(promptDefault(in, "名前（esa sync <名前> で使う）", t.Name))

	// esa に問い合わせる前に、保存したときと同じ検証を通す（名前の重複・dir の重なりを使い方エラーで先に止める）。
	if err := validateSyncTargets(append(append([]syncTarget(nil), targets...), t)); err != nil {
		return &usageError{"エラー: " + err.Error()}
	}

	previewSyncCategory(cfg, t.Category, os.Stdout)

	if err := appendSyncTarget(path, t); err != nil {
		return err
	}
	fmt.Printf("\n保存しました: %s\n", path)
	fmt.Printf("  name=%s  category=%s  dir=%s\n", t.Name, t.Category, t.Dir)
	fmt.Println("\n次に差分を確認してから書き込んでください:")
	fmt.Printf("  esa sync %s          # 差分を表示（書き込まない）\n", t.Name)
	fmt.Printf("  esa sync %s --apply  # 書き込む\n", t.Name)
	return nil
}

// parseCategoryInput はカテゴリの入力を正規化する。esa のカテゴリ一覧の URL（https://<team>.esa.io/#path=%2FUsers%2Fme）も受け付ける。
//
// 🚨 #path= の無い URL（記事の URL 等）を黙ってカテゴリ名として保存しない。以前は URL がそのままカテゴリになり、
// 「記事が見つかりません。登録はします」で rc=0、以後の sync も 0 件のまま気づけなかった。
// URL のチームが設定中の team と違う場合も止める（検索は設定中の team に対して行うため）。
func parseCategoryInput(s, team string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return normalizeCategory(s), nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", &usageError{fmt.Sprintf("エラー: URL として解釈できません: %q", s)}
	}
	raw, ok := strings.CutPrefix(u.EscapedFragment(), "path=") // Fragment はデコード済みなので、もう一度デコードすると % を含む名前が壊れる
	if !ok {
		return "", &usageError{fmt.Sprintf("エラー: カテゴリの URL ではありません: %q\n"+
			"  esa でカテゴリを開いたときの https://<team>.esa.io/#path=... の URL を貼るか、カテゴリ名（例: Users/me/skills）を入力してください。", s)}
	}
	if j := strings.IndexByte(raw, '&'); j >= 0 {
		raw = raw[:j]
	}
	cat, err := url.PathUnescape(raw) // + を空白にしない（QueryUnescape は + を空白に変える）
	if err != nil {
		return "", &usageError{fmt.Sprintf("エラー: URL の #path= を解釈できません: %q", s)}
	}
	if host, found := strings.CutSuffix(strings.ToLower(u.Hostname()), ".esa.io"); found && team != "" && host != strings.ToLower(team) {
		return "", &usageError{fmt.Sprintf("エラー: URL のチーム %q が、設定中のチーム %q と違います（-team で指定してください）", host, team)}
	}
	return normalizeCategory(cat), nil
}

// defaultSyncName はカテゴリの末尾を名前の既定にする（名前に使えない文字は - に置き換える）。
func defaultSyncName(category string) string {
	base := path.Base(category)
	var sb strings.Builder
	for _, r := range base {
		if r < 0x80 && (r == '.' || r == '_' || r == '-' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')) {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('-')
		}
	}
	name := strings.Trim(sb.String(), "-._")
	if name == "" || syncReservedNames[name] {
		return ""
	}
	return name
}

// previewSyncCategory はカテゴリ配下の記事の件数を表示する。確認できなくても保存は止めない
// （Chrome のセッション切れ等でも、登録だけは先にできるように）。
func previewSyncCategory(cfg config, category string, w io.Writer) {
	fmt.Fprintf(w, "\nesa で %q 配下の記事を確認しています...\n", category)
	c, err := buildCookieClient(cfg)
	if err == nil {
		var numbers []int
		numbers, err = c.listCategoryPosts(category)
		if err == nil {
			if len(numbers) == 0 {
				fmt.Fprintln(w, "注意: 記事が 1 件も見つかりませんでした（カテゴリ名を確かめてください）。登録はします。")
			} else {
				fmt.Fprintf(w, "記事が %d 件見つかりました（前方一致の件数。隣のカテゴリの記事は sync のときに除きます）。\n", len(numbers))
			}
			return
		}
	}
	fmt.Fprintf(w, "警告: esa の記事を確認できませんでした（登録はします）: %v\n", err)
}
