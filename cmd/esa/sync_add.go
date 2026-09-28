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
  -category <カテゴリ>  esa のカテゴリ。https://<team>.esa.io/#path=... の URL をそのまま貼ってもよい
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

	t.Category = parseCategoryInput(promptDefault(in, "esa のカテゴリ（URL を貼ってもよい）", parseCategoryInput(t.Category)))
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

	existing := map[string]bool{}
	for _, e := range targets {
		existing[e.Name] = true
	}
	if err := validateSyncTarget(t, existing); err != nil {
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

// parseCategoryInput はカテゴリの入力を正規化する。esa の URL（…/#path=%2FUsers%2Fme）も受け付ける。
func parseCategoryInput(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "#path="); i >= 0 {
		raw := s[i+len("#path="):]
		if j := strings.IndexByte(raw, '&'); j >= 0 {
			raw = raw[:j]
		}
		if dec, err := url.QueryUnescape(raw); err == nil {
			s = dec
		}
	}
	return normalizeCategory(s)
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
