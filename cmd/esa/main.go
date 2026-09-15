// Command esa は esa.io の任意チームのドキュメントを Chrome のログインセッション
// Cookie で参照する読み取り専用 CLI。Claude Code から esa の記事を検索・参照するために使う。
// 対象チームは -team / ESA_TEAM / config.yml(team) で指定する（特定チームに依存しない）。
//
// 認証: Chrome の Cookie を Keychain 経由で復号して利用する（トークン発行不要）。
// パスはすべて HOME 基準で解決し、カレントディレクトリに一切依存しない。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"strconv"
	"strings"
)

// config は解決済みの実行設定。
type config struct {
	team    string // チーム名（サブドメイン）例: myteam（https://myteam.esa.io の myteam 部分）
	browser string // Chrome / Brave / ...
	profile string // Default / Profile 1 / ...
	asJSON  bool
}

func (c config) teamHost() string { return c.team + ".esa.io" }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// registerCommon は全サブコマンド共通のフラグを登録する。
// 既定値は「環境変数 > config.yml > 組み込み既定」で解決し、-flag 明示指定が最優先になる。
func registerCommon(fs *flag.FlagSet, cfg *config) {
	fc := loadFileConfig()
	fs.StringVar(&cfg.team, "team", resolveDefault("ESA_TEAM", fc.Team, ""), "esa チーム名（サブドメイン。必須）https://<team>.esa.io の <team> / ESA_TEAM / config.yml team")
	fs.StringVar(&cfg.browser, "browser", resolveDefault("ESA_BROWSER", fc.Browser, "Chrome"), "Cookie を読むブラウザ（Chrome/Brave/Chromium/Edge/Vivaldi）/ ESA_BROWSER / config.yml browser")
	fs.StringVar(&cfg.profile, "profile", resolveDefault("ESA_CHROME_PROFILE", fc.Profile, "auto"), "ブラウザのプロファイル名。既定 auto（自動検出）/ ESA_CHROME_PROFILE / config.yml profile")
	fs.BoolVar(&cfg.asJSON, "json", false, "機械可読な JSON で出力する")
}

// newFlagSet は共通の Usage（サブコマンド詳細 help）を設定した FlagSet を作る。
func newFlagSet(name, help string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stdout, help) }
	return fs
}

// topUsage は `esa` / `esa --help` の出力（サブコマンド一覧 + 1 行概要 + 共通事項）。
const topUsage = `esa - <team>.esa.io（社内 esa）ドキュメント参照 CLI（読み取り専用 / Chrome cookie 認証）

概要:
  社内 esa の記事を検索して本文を読むためのコマンド。ネット上に無い社内情報（手順書・規程・
  インシデント記録・設計メモ等）を調べるのに使う。更新系は無い（読み取り専用）。
  認証は自動：ログイン済みの Chrome プロファイルを自動検出するので鍵やトークンの指定は不要。
  基本は 2 段階 — esa search '<クエリ>' で記事番号を得て、esa show <番号> で本文を読む。

サブコマンド:
  search      記事を検索して一覧表示（TSV。表示カラムは -c で変更可）
  show        記事本文を Markdown（front matter 付き）で出力
  meta        記事のメタ情報を出力（-comments でコメントも）
  revisions   リビジョン一覧を出力
  config      設定ファイル(config.yml)の表示・編集（使用プロファイル等を保存）
  setup       対話式セットアップ（team/プロファイル等を保存）
  help        このヘルプ

各サブコマンドの詳細:  esa <サブコマンド> --help   （例: esa search --help）

共通オプション（全サブコマンド）:
  -team <name>     チーム名（サブドメイン）。必須。https://<team>.esa.io の <team>（ESA_TEAM / config でも可）
  -browser <name>  Cookie を読むブラウザ Chrome/Brave/Chromium/Edge/Vivaldi。既定 Chrome（ESA_BROWSER）
  -profile <name>  プロファイル。既定 auto=ログイン済みを自動検出（ESA_CHROME_PROFILE）
  -json            JSON で出力（search / meta / revisions。show は常に Markdown）

設定の優先順位: コマンドラインフラグ > 環境変数 > config.yml > 既定
  よく使う値（使用プロファイル等）は config.yml に保存できる。詳細は esa config --help
  例: esa config set profile "Profile 3"   /   esa config init（自動検出して保存）

引数:
  <番号> は記事 URL 末尾の数値。URL をそのまま渡してもよい
  （例: esa show https://<team>.esa.io/posts/28025）

終了コード: 0=成功 / 1=実行時エラー(認証切れ・404・ネットワーク等) / 2=使い方の誤り
  エラーは stderr に「エラー: ...」で出力。

認証:
  対象 Chrome で https://<team>.esa.io にログインしている必要がある。セッション切れだと
  内部エンドポイントは全パス 404 になる（非公開チームの挙動）→ Chrome で入り直す。
  初回は macOS の Keychain 許可ダイアログで「常に許可」を選ぶ。
`

// searchHelp は `esa search --help` の詳細。
const searchHelp = `esa search - 記事を検索する（結果は TSV。表示カラムは自由に選べる）

使い方:
  esa search [オプション] <クエリ...>

  クエリは複数語をそのまま並べてよい（内部でスペース連結）。
  例: esa search in:設計 パスワード

オプション:
  -c, -columns <list>  表示カラム（カンマ区切り）。既定: number,created,updated,author,name
  -no-header           ヘッダ行を出さない（awk -F'\t' 等でパースしやすい）
  -fast                各記事の詳細取得を省略して高速化（number/title/url のみ確実。日付/author は空）
  -n <数>              取得件数目安（ESA_TOKEN 使用時の per_page、最大 100）
  -page <数>           ページ番号
  -json                各記事オブジェクトの配列を JSON で出力
  （共通オプション -team/-browser/-profile は esa --help を参照）

指定可能なカラム（-c / -columns）:
  number      記事番号
  name        カテゴリ/タイトル（full_name 例: カテゴリ/サブカテゴリ/タイトル）
  title       タイトルのみ
  category    カテゴリ
  url         URL
  created     作成日 (YYYY-MM-DD)       created_at  作成日時 (ISO8601)
  updated     更新日 (YYYY-MM-DD)       updated_at  更新日時 (ISO8601)
  author      最終更新者 screen_name    updated_by  同左
  created_by  作成者 screen_name
  wip         WIP か (true/false)       tags        タグ(カンマ連結)
  ※ 日付・author・tags は各記事 JSON を並行取得して埋めるため、多数ヒット時はやや遅い。
    速度優先なら -fast。ESA_TOKEN 設定時は公式 API が 1 リクエストで全カラムを高速取得。

検索クエリ q の構文（esa の Web 検索と同一）:
  キーワード      仕様書            タイトル/カテゴリ/本文をあいまい検索
  "フレーズ"      "完全一致 フレーズ"  フレーズ完全一致
  a b             Slack Teams                 AND（スペース区切り）
  a OR b          Slack OR Teams              OR
  -語             -退職                       除外(NOT)
  (a OR b) c      (Slack OR Teams) -退職      グルーピング
  絞り込み:  title: name:（タイトル） body:（本文） full_name:（カテゴリ/タイトル） comment:（コメント）
  カテゴリ:  category:（部分一致） in:（前方一致=配下） on:（完全一致）
  タグ:      #tag / tag:tag
  人:        @screen_name / user:  updated_by:  watched_by:
  状態:      wip:true|false  kind:stock|flow  starred:true  watched:true  sharing:true
  数値:      stars:>3  watches:>4  comments:>5  backlinks:>5  done:>=6  undone:>0
  日付:      created:>2026-01-01  updated:>2026-08（年月だけでも可）
  並び替え:  sort:updated-desc / created-asc / best_match-desc / stars-desc / number-desc など

-json のオブジェクトスキーマ（主なキー）:
  number:int, full_name:str, name:str, category:str, url:str,
  created_at:str(ISO), updated_at:str(ISO), created_by:str, updated_by:str, wip:bool, tags:[str]

例:
  esa search 'in:設計 updated:>2026-01-01'
  esa search -c number,updated,author,name 'キーワード'
  esa search -c number,created,updated,created_by,updated_by,url 'title:ガイドライン'
  esa search -no-header -c number,url 'キーワード' | awk -F'\t' '{print $2}'
  esa search -json 'in:設計' | jq -r '.[].number'
`

// showHelp は `esa show --help` の詳細。
const showHelp = `esa show - 記事本文を Markdown で出力する

使い方:
  esa show <番号|URL>

出力:
  YAML front matter（title/category/tags/created_at/updated_at/number 等）+ 本文の Markdown。
  そのまま grep や less、glow に流せる。-json は無効（show は常に Markdown）。
  （共通オプション -team/-browser/-profile は esa --help を参照）

例:
  esa show 28025
  esa show https://<team>.esa.io/posts/28025
  esa show 28025 | sed -n '1,120p'     # 長い記事は範囲を絞る
  esa show 28025 | glow -              # 色付きで読む
`

// metaHelp は `esa meta --help` の詳細。
const metaHelp = `esa meta - 記事のメタ情報を出力する

使い方:
  esa meta [オプション] <番号|URL>

オプション:
  -comments   コメントも取得して表示する
  -json       記事オブジェクト全体を JSON で出力
  （共通オプション -team/-browser/-profile は esa --help を参照）

既定の表示（読みやすい key: value 形式）:
  number / full_name / wip / category / tags / created_at / updated_at /
  updated_by / message / url

例:
  esa meta 28025
  esa meta 28025 -comments
  esa meta 28025 -json | jq '.updated_by.screen_name'
`

// revisionsHelp は `esa revisions --help` の詳細。
const revisionsHelp = `esa revisions - 記事のリビジョン一覧を出力する

使い方:
  esa revisions <番号|URL>

出力:
  「リビジョン番号 / 更新日時 / 更新者 screen_name」をタブ区切りで（新しい順）。
  -json で生の JSON を出力。
  （共通オプション -team/-browser/-profile は esa --help を参照）

例:
  esa revisions 28025
  esa revisions 28025 -json
`

func main() {
	// Chrome の Cookie DB の一時コピーを、Ctrl-C でも残さないようにする（cookies.go の②）。
	installCleanupOnSignal()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, topUsage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "search":
		err = cmdSearch(args)
	case "show", "cat":
		err = cmdShow(args)
	case "meta":
		err = cmdMeta(args)
	case "revisions", "rev":
		err = cmdRevisions(args)
	case "config":
		err = cmdConfig(args)
	case "setup":
		err = cmdSetup(args)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, topUsage)
		return
	default:
		fmt.Fprintf(os.Stderr, "不明なコマンド: %q\n\n%s", cmd, topUsage)
		os.Exit(2)
	}

	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			// 使い方の誤り: メッセージをそのまま出して終了コード 2。
			fmt.Fprintln(os.Stderr, ue.Error())
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "エラー: "+err.Error())
		os.Exit(1)
	}
}

// usageError は「引数の指定ミス」を表す。main で終了コード 2 として扱う。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// requireTeam は team が未設定なら使い方エラーを返す（特定チームに依存させないため既定を持たない）。
func (c config) requireTeam() error {
	if strings.TrimSpace(c.team) == "" {
		return &usageError{"エラー: チーム名(team)が未設定です。esa の https://<team>.esa.io の <team> を指定してください:\n" +
			"  esa config set team <team>   （推奨: 一度設定すれば以後不要）\n" +
			"  export ESA_TEAM=<team>\n" +
			"  esa <コマンド> -team <team> ..."}
	}
	return nil
}

// buildCookieClient は実効プロファイルを解決してクライアントを構築する（auto なら自動検出）。
func buildCookieClient(cfg config) (*client, error) {
	return resolveProfile(cfg)
}

func cmdSearch(args []string) error {
	var cfg config
	var perPage, page int
	var columnsSpec string
	var fast, noHeader bool
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stdout, searchHelp) }
	registerCommon(fs, &cfg)
	fs.IntVar(&perPage, "n", 50, "取得件数（公式 API 使用時の per_page。最大 100）")
	fs.IntVar(&page, "page", 1, "ページ番号")
	fs.StringVar(&columnsSpec, "columns", defaultColumns, "表示カラム（カンマ区切り）。指定可能: "+availableColumns())
	fs.StringVar(&columnsSpec, "c", defaultColumns, "-columns の別名")
	fs.BoolVar(&fast, "fast", false, "詳細取得(各記事JSON)を省略して高速化（number/title/url のみ確実）")
	fs.BoolVar(&noHeader, "no-header", false, "ヘッダ行を出力しない")
	fs.Parse(args)

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return &usageError{"エラー: 検索クエリを指定してください。\n使い方: esa search [オプション] <クエリ...>\n例:     esa search 'in:設計 キーワード'\n詳細:   esa search --help"}
	}
	if perPage > 100 {
		perPage = 100
	}

	cols, err := parseColumns(columnsSpec)
	if err != nil {
		return err
	}

	if err := cfg.requireTeam(); err != nil {
		return err
	}

	token := os.Getenv("ESA_TOKEN")

	var results []searchResult
	if token != "" {
		// 公式 API は 1 リクエストで詳細まで返るため enrich 不要。
		results, err = searchViaAPI(cfg.team, query, perPage, page, token)
	} else {
		c, cerr := buildCookieClient(cfg)
		if cerr != nil {
			return cerr
		}
		// カラムが詳細を要する場合のみ enrich（-fast で無効化）。
		needEnrich := !fast && columnsNeedEnrich(cols)
		results, err = c.search(cfg.team, query, perPage, page, "", needEnrich)
	}
	if err != nil {
		return err
	}

	if cfg.asJSON {
		return printJSON(results)
	}
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "0 件")
		return nil
	}
	fmt.Print(renderTable(results, cols, !noHeader))
	return nil
}

func cmdShow(args []string) error {
	var cfg config
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stdout, showHelp) }
	registerCommon(fs, &cfg)
	fs.Parse(args)

	number, err := parseNumberArg(fs.Args(), "show")
	if err != nil {
		return err
	}
	c, err := buildCookieClient(cfg)
	if err != nil {
		return err
	}
	md, err := c.postMD(number)
	if err != nil {
		return err
	}
	fmt.Print(md)
	if !strings.HasSuffix(md, "\n") {
		fmt.Println()
	}
	return nil
}

func cmdMeta(args []string) error {
	var cfg config
	var withComments bool
	fs := flag.NewFlagSet("meta", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stdout, metaHelp) }
	registerCommon(fs, &cfg)
	fs.BoolVar(&withComments, "comments", false, "コメントも取得する")
	fs.Parse(args)

	number, err := parseNumberArg(fs.Args(), "meta")
	if err != nil {
		return err
	}
	c, err := buildCookieClient(cfg)
	if err != nil {
		return err
	}
	post, err := c.postJSON(number, withComments)
	if err != nil {
		return err
	}

	if cfg.asJSON {
		return printJSON(post)
	}
	// 主要フィールドを読みやすく出力する。
	printMetaField(post, "number")
	printMetaField(post, "full_name")
	printMetaField(post, "wip")
	printMetaField(post, "category")
	printMetaField(post, "tags")
	printMetaField(post, "created_at")
	printMetaField(post, "updated_at")
	if u, ok := post["updated_by"].(map[string]any); ok {
		fmt.Printf("%-14s %v\n", "updated_by:", u["screen_name"])
	}
	printMetaField(post, "message")
	printMetaField(post, "url")
	if withComments {
		if cs, ok := post["comments"].([]any); ok {
			fmt.Printf("\n--- コメント %d 件 ---\n", len(cs))
			for _, ci := range cs {
				if cm, ok := ci.(map[string]any); ok {
					var who string
					if by, ok := cm["created_by"].(map[string]any); ok {
						who, _ = by["screen_name"].(string)
					}
					fmt.Printf("[%v] %s:\n%v\n\n", cm["created_at"], who, cm["body_md"])
				}
			}
		}
	}
	return nil
}

func cmdRevisions(args []string) error {
	var cfg config
	fs := flag.NewFlagSet("revisions", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stdout, revisionsHelp) }
	registerCommon(fs, &cfg)
	fs.Parse(args)

	number, err := parseNumberArg(fs.Args(), "revisions")
	if err != nil {
		return err
	}
	c, err := buildCookieClient(cfg)
	if err != nil {
		return err
	}
	data, err := c.revisionsJSON(number)
	if err != nil {
		return err
	}
	if cfg.asJSON {
		return printJSON(data)
	}
	if revs, ok := data["revisions"].([]any); ok {
		for _, ri := range revs {
			if rv, ok := ri.(map[string]any); ok {
				var who string
				if by, ok := rv["updated_by"].(map[string]any); ok {
					who, _ = by["screen_name"].(string)
				}
				fmt.Printf("%v\t%v\t%s\n", rv["number"], rv["updated_at"], who)
			}
		}
		return nil
	}
	return printJSON(data)
}

func parseNumberArg(args []string, cmd string) (int, error) {
	if len(args) == 0 {
		return 0, &usageError{fmt.Sprintf(
			"エラー: 記事番号を指定してください。\n使い方: esa %s <番号|URL>\n例:     esa %s 28025\n詳細:   esa %s --help",
			cmd, cmd, cmd)}
	}
	// URL 末尾の番号も受け付ける。
	raw := args[0]
	if i := strings.LastIndex(raw, "/posts/"); i >= 0 {
		raw = raw[i+len("/posts/"):]
	}
	raw = strings.TrimSuffix(raw, ".md")
	raw = strings.TrimSuffix(raw, ".json")
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("記事番号として解釈できません: %q", args[0])
	}
	return n, nil
}

func printMetaField(m map[string]any, key string) {
	v, ok := m[key]
	if !ok || v == nil {
		return
	}
	// esa は name/full_name 内の "/" を &#47;、"#" を &#35; にエスケープして返すため復元する。
	if sv, ok := v.(string); ok {
		v = html.UnescapeString(sv)
	}
	fmt.Printf("%-14s %v\n", key+":", v)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
