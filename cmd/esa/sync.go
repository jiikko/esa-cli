package main

import (
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const syncHelp = `esa sync - esa のカテゴリ配下の記事を、ローカルのディレクトリへ書き出す（esa → ローカルの一方向）

使い方:
  esa sync [オプション] [名前...]   config.yml の sync: の対象の差分を表示する（既定は dry-run。書き込まない）
  esa sync --apply [名前...]       差分を表示してから書き込む（書くのはその時点の esa の内容。
                                   dry-run の後に esa 側が変わっていれば、変わった内容が書かれる）
  esa sync add                     対象を対話式で追加する（esa sync add --help）
  esa sync list                    登録済みの対象を一覧する
  esa sync help                    このヘルプ

  名前を省くと全対象。フラグは名前の前後どちらに書いてもよい。

オプション:
  -apply   書き込む（付けなければ差分の表示だけ）

対応の規則（category: Users/me/skills、dir: ~/.claude/skills の場合）:
  Users/me/skills/foo/SKILL   → ~/.claude/skills/foo/SKILL.md
  Users/me/skills/README      → ~/.claude/skills/README.md
  Users/me/skills/foo/run.md  → ~/.claude/skills/foo/run.md   （.md で終わる名前はそのまま）
  書き出し先の指定のある記事は、記事名に関係なく、記事のカテゴリのディレクトリからその相対パスへ書く
  （例: Users/me/skills の記事で foo/SKILL.md → ~/.claude/skills/foo/SKILL.md）。書き方は 2 通り（両方はエラー）:
    - skill の front matter の metadata の下に esa-sync: foo/SKILL.md（front matter はそのまま書き出す）
    - 本文の最後の行に <!-- esa-sync: foo/SKILL.md -->（その行は書き出すファイルから取り除く）
  パスの代わりに skip と書いた記事（<!-- esa-sync: skip --> / metadata の esa-sync: skip）は書き出さない
  （esa の上だけで読む説明の記事など。dry-run と --apply に「書き出さない」と出る。記事名のパスのローカルのファイルには触らない）。
  書き出す記事の本文に skip の指定らしい行があるのに効いていなければ、「注意:」を出す。
  指定らしいのに形が崩れたもの（metadata の外の esa-sync・崩れたコメントなど）はエラーにする。
  中身は記事本文の Markdown（esa の記事情報を front matter として足さない。改行は LF にそろえる）。WIP の記事も対象。

書き込みの規則:
  - 書くのは esa 側にある記事のファイルだけ。esa で消した記事のファイルは消さない
  - ローカルで編集したファイルも上書きする（dry-run の差分に出るので、先に確認すること）。
    差分を表示してから書くまでの間にローカルが変わったら、そのファイルは書かずにエラーにする
    （書く直前の読み直しから置き換えまでの一瞬の編集だけは防げない）
  - 同じ dir への --apply は同時に 1 本だけ（2 本目はエラー）。中断で残った一時ファイル（*.esa-sync-tmp）は
    dry-run で知らせ、次の --apply で消す。見るのは今回書き出す記事のファイルの分だけで、中断の後に esa で
    改名・削除した記事の一時ファイルは残る（手で消してよい）
  - 書き出し先がシンボリックリンク・ディレクトリなら、その対象は 1 件も書かずにエラーにする
  - dir の外へ出るパス（.. 等）になる記事があれば、その対象は 1 件も書かずにエラーにする
  - 大文字小文字・Unicode の正規化だけが違う 2 記事（Foo と foo 等）は同じファイルとみなしてエラーにする
    （macOS の既定のファイルシステムでは同じファイルになるため）
  - 差分の表示では制御文字を \x{1b} のようにエスケープする（端末の表示で本文を隠せないように）

設定ファイル: $XDG_CONFIG_HOME/esa-cli/config.yml（未設定なら ~/.config/esa-cli/config.yml）の sync:
  （profile / team と同じファイル。v0.1.8 までの sync.yml は読まない。残っていればエラーで移し方を案内する）
  sync:
    - name: skills
      category: Users/me/skills
      dir: ~/.claude/skills

例:
  esa sync add
  esa sync                 # 全対象の差分を確認
  esa sync skills --apply  # 1 対象だけ書き込む
` + commonOptionsHelp + commonTailHelp

// maxSyncPages は検索のページ送りの上限。超えたら切り詰めた一覧で書き出さずにエラーにする。
const maxSyncPages = 200

func cmdSync(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return syncAdd(args[1:])
		case "list":
			return syncList(args[1:])
		case "help": // esa config help と揃える（help は対象の名前に使えない予約名）
			fmt.Fprint(os.Stdout, syncHelp)
			return nil
		}
	}
	var cfg config
	var apply bool
	fs := newFlagSet("sync")
	registerCommon(fs, &cfg)
	fs.BoolVar(&apply, "apply", false, "書き込む（付けなければ差分の表示だけ）")
	names, done, err := parsePositionals(fs, syncHelp, args)
	if err != nil || done {
		return err
	}
	for _, n := range names {
		// `esa sync -apply add` のようにフラグの後ろに書くと、add が対象の名前として扱われる。予約名は対象に使えないので、
		// 「登録されていません」ではなく書き方の誤りとして返す。
		if syncReservedNames[n] {
			return &usageError{fmt.Sprintf("エラー: サブコマンド %q は esa sync の直後に書いてください（例: esa sync %s ...）", n, n)}
		}
	}
	targets, path, err := loadSyncTargets()
	if err != nil {
		return err
	}
	selected, err := selectSyncTargets(targets, names, path)
	if err != nil {
		return err
	}
	c, err := buildCookieClient(cfg)
	if err != nil {
		return err
	}

	var failed []string
	pending := 0
	for _, t := range selected {
		plan, err := runSyncTarget(c, t, apply, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "エラー: [%s] %v\n", t.Name, err)
			failed = append(failed, t.Name)
		}
		pending += plan.pending()
	}
	if !apply && pending > 0 {
		fmt.Println("\ndry-run です（書き込んでいません）。書き込むには --apply を付けてください。")
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d 件の対象が失敗しました: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

func selectSyncTargets(targets []syncTarget, names []string, path string) ([]syncTarget, error) {
	if len(targets) == 0 {
		return nil, &usageError{fmt.Sprintf("エラー: sync の対象が登録されていません（%s）。\n  esa sync add で追加してください。", path)}
	}
	if len(names) == 0 {
		return targets, nil
	}
	byName := map[string]syncTarget{}
	var known []string
	for _, t := range targets {
		byName[t.Name] = t
		known = append(known, t.Name)
	}
	var out []syncTarget
	for _, n := range names {
		t, ok := byName[n]
		if !ok {
			return nil, &usageError{fmt.Sprintf("エラー: 対象 %q は登録されていません（登録済み: %s）", n, strings.Join(known, ", "))}
		}
		out = append(out, t)
	}
	return out, nil
}

// syncListHelp は `esa sync list --help` の出力。
const syncListHelp = `esa sync list - 登録済みの esa sync の対象を一覧する

使い方:
  esa sync list

出力は 1 対象 1 行の TSV（名前・カテゴリ・書き出し先）。対象は config.yml の sync: に書く（esa sync add で追加できる）。
対象が無ければ何も出さず、stderr に「登録なし」と出す。esa には問い合わせない。

終了コード: 0=成功 / 1=config.yml を読めない等 / 2=使い方の誤り
`

func syncList(args []string) error {
	fs := newFlagSet("sync list")
	positional, done, err := parsePositionals(fs, syncListHelp, args)
	if err != nil || done {
		return err
	}
	if len(positional) > 0 {
		return &usageError{"エラー: esa sync list は引数を取りません。"}
	}
	targets, path, err := loadSyncTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "登録なし（%s）。esa sync add で追加できます。\n", path)
		return nil
	}
	for _, t := range targets {
		fmt.Printf("%s\t%s\t%s\n", t.Name, t.Category, t.Dir)
	}
	return nil
}

// syncFile は書き出す 1 ファイル。
type syncFile struct {
	rel         string // dir からの相対パス（/ 区切り）
	number      int
	byDirective bool     // 書き出し先を本文の指定（esa-sync:）で決めた
	catDir      string   // 記事のカテゴリに対応するディレクトリ（dir からの相対。指定のある記事で、どこからが指定かを見分ける）
	warns       []string // dry-run と --apply の出力に添える注意（syncSkillWarning / syncSkipIntentWarning）
	body        string
	updatedBy   string
	updatedAt   string
}

// syncSkipSpec は書き出し先の指定の値で「書き出さない」を表す（issue 015）。パスは .md で終わるので取り違えない。
const syncSkipSpec = "skip"

// syncSkipped は書き出し先の指定が skip の、書き出さない記事。
type syncSkipped struct {
	number         int
	category, name string
}

type syncStatus int

const (
	syncSame syncStatus = iota
	syncNew
	syncChanged
)

// syncPlan は 1 対象の計画。書き出し先の今の内容と比べた状態を、ファイルごとに持つ。
type syncPlan struct {
	items     []syncPlanItem
	skipped   []syncSkipped // 書き出さない記事（表示するだけ。書き込み（予定）の件数に数えない）
	leftovers []string      // 前回中断したときの一時ファイル（計画に載っているファイルの分だけ。走査はしない）
}

type syncPlanItem struct {
	f      syncFile
	status syncStatus
	old    string // 計画のときの書き出し先の内容（書く直前にこれと一致するかを確かめる）
}

// pending は書き込み（予定）の件数。
func (p syncPlan) pending() int {
	n := 0
	for _, it := range p.items {
		if it.status != syncSame {
			n++
		}
	}
	return n
}

// runSyncTarget は 1 対象の差分を表示し、apply なら書き込む。
//
// 🚨 一覧・取得・対応付け・書き出し先の検査を全部終えてから書き始める。途中で 1 件でも問題があれば
// 1 件も書かない（esa の一覧を半分だけ反映した状態を作らない）。
func runSyncTarget(c *client, t syncTarget, apply bool, w io.Writer) (syncPlan, error) {
	dir, err := expandDir(t.Dir)
	if err != nil {
		return syncPlan{}, err
	}
	fmt.Fprintf(w, "[%s] %s → %s\n", t.Name, t.Category, dir)

	// 計画から書き込みまでを、同じ dir への他の --apply と排他にする（dry-run は書かないので取らない）。
	if apply {
		lock, err := acquireSyncLock(dir)
		if err != nil {
			return syncPlan{}, err
		}
		defer lock.release()
	}

	numbers, posts, err := c.fetchCategoryPosts(t.Category)
	if err != nil {
		return syncPlan{}, err
	}
	files, skipped, excluded, err := mapSyncFiles(t.Category, numbers, posts)
	if err != nil {
		return syncPlan{}, err
	}
	if len(files) == 0 && len(skipped) == 0 && excluded > 0 {
		fmt.Fprintf(w, "  注意: 検索に当たった %d 件はどれもカテゴリが %q と一致しないため除きました（大文字小文字・表記を esa と揃えてください）\n",
			excluded, t.Category)
	}

	root, err := openSyncRoot(dir)
	if err != nil {
		return syncPlan{}, err
	}
	if root != nil {
		defer root.Close()
	}
	plan, err := planSync(root, files)
	if err != nil {
		return syncPlan{}, err
	}
	plan.skipped = skipped
	printSyncPlan(w, plan, apply)
	if !apply {
		return plan, nil
	}
	if root == nil { // 書き出し先がまだ無い（計画では全件が新規）。作るのは書くと決まってから
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return plan, err
		}
		if root, err = openSyncRoot(dir); err != nil {
			return plan, err
		}
		if root == nil { // 作った直後に消された
			return plan, fmt.Errorf("書き出し先 %s を作った直後に開けませんでした（消された可能性があります）", dir)
		}
		defer root.Close()
	}
	return plan, applySyncPlan(root, plan, w)
}

// planSync は書き出し先の今の内容と比べて、ファイルごとの状態（新規・変更・変更なし）を決める。
// root が nil（書き出し先がまだ無い）なら全件が新規。書き出し先に 1 件でも問題があれば計画にしない。
func planSync(root *os.Root, files []syncFile) (syncPlan, error) {
	var plan syncPlan
	var problems []string
	for _, f := range files {
		cur, err := readSyncDest(root, f.rel)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", visible(f.rel), err))
			continue
		}
		if root != nil {
			if _, err := root.Lstat(filepath.FromSlash(f.rel) + syncTmpSuffix); err == nil {
				plan.leftovers = append(plan.leftovers, f.rel+syncTmpSuffix)
			}
		}
		st := syncSame
		switch {
		case !cur.exists:
			st = syncNew
		case cur.content != f.body:
			st = syncChanged
		}
		plan.items = append(plan.items, syncPlanItem{f, st, cur.content})
	}
	if len(problems) > 0 {
		return syncPlan{}, fmt.Errorf("書き出し先に問題があるため、この対象は 1 件も書き込みません:\n  %s", strings.Join(problems, "\n  "))
	}
	return plan, nil
}

// printSyncPlan は計画（新規の本文・変更の差分・件数・残骸）を表示する。
func printSyncPlan(w io.Writer, plan syncPlan, apply bool) {
	var nNew, nChanged, nSame int
	for _, it := range plan.items {
		src := fmt.Sprintf("esa #%d", it.f.number)
		if it.f.byDirective {
			src += "（書き出し先は本文の指定による）"
		}
		if it.f.updatedBy != "" {
			src += visible(fmt.Sprintf("（%s %s）", it.f.updatedBy, it.f.updatedAt))
		}
		rel := visible(it.f.rel)
		var diff string
		switch it.status {
		case syncNew:
			nNew++
			fmt.Fprintf(w, "  + %s  新規 %s\n", rel, src)
			// 新規も本文を差分として出す（出さないと、新しく足された記事の中身を dry-run で確認できない）
			diff = newFileSummary(it.f.body, src)
		case syncChanged:
			nChanged++
			fmt.Fprintf(w, "  ~ %s  変更 %s\n", rel, src)
			diff = unifiedDiff(it.old, it.f.body, "ローカル "+rel, src, 3)
		default:
			nSame++
		}
		for _, line := range splitLines(diff) {
			fmt.Fprintf(w, "    %s\n", visible(line))
		}
		// 変更なしの記事にも出す（書き損じのまま一度書き出した後も、気づけるように）
		for _, warn := range it.f.warns {
			fmt.Fprintf(w, "  注意: %s（esa #%d）: %s\n", rel, it.f.number, warn)
		}
	}
	// 🚨 書き出さない記事も必ず出す（黙って飛ばすと、指定が効いたのか記事が一覧に無いのかを見分けられない）
	for _, sk := range plan.skipped {
		fmt.Fprintf(w, "  - %s  書き出さない esa #%d（書き出し先の指定 esa-sync: %s）\n", visible(sk.category+"/"+sk.name), sk.number, syncSkipSpec)
	}
	summary := fmt.Sprintf("  記事 %d 件: 新規 %d / 変更 %d / 変更なし %d", len(plan.items), nNew, nChanged, nSame)
	if len(plan.skipped) > 0 {
		summary += fmt.Sprintf(" / 書き出さない %d", len(plan.skipped))
	}
	fmt.Fprintln(w, summary)
	if !apply && len(plan.leftovers) > 0 {
		fmt.Fprintf(w, "  注意: 前回中断したとき（か、いま別の --apply が書いている）一時ファイルが %d 件あります（--apply で消します）: %s\n",
			len(plan.leftovers), visible(strings.Join(plan.leftovers, ", ")))
	}
}

// applySyncPlan は計画どおりに書き込む。呼び出し側は acquireSyncLock を持っていること。
func applySyncPlan(root *os.Root, plan syncPlan, w io.Writer) error {
	// 🚨 ロックの下なので、計画に載っているファイルの一時ファイルは前回の自分の残骸と決まる（並行する他の
	// --apply のものではない）。変更なしのファイルの分も消す（消さないと、そのファイルが変わるまで残り続ける）。
	// 一時ファイルを消すのはここだけ（writeSyncFile は O_EXCL で作るので、残っていれば失敗して止まる）。
	for _, l := range plan.leftovers {
		if err := root.Remove(filepath.FromSlash(l)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("前回の一時ファイル %s を消せません: %w", visible(l), err)
		}
	}
	if len(plan.leftovers) > 0 {
		fmt.Fprintf(w, "  前回中断したときの一時ファイルを %d 件消しました\n", len(plan.leftovers))
	}
	written := 0
	for _, it := range plan.items {
		if it.status == syncSame {
			continue
		}
		if err := writeSyncFile(root, it.f, it.old, it.status == syncChanged); err != nil {
			return fmt.Errorf("%s の書き込みに失敗しました（%d 件は書き込み済み）: %w", visible(it.f.rel), written, err)
		}
		written++
	}
	if written > 0 {
		fmt.Fprintf(w, "  %d 件を書き込みました\n", written)
	}
	return nil
}

// fetchCategoryPosts はカテゴリ配下の記事を一覧し、各記事の JSON を取得する（1 件でも取れなければエラー）。
func (c *client) fetchCategoryPosts(category string) (numbers []int, posts []map[string]any, err error) {
	numbers, err = c.listCategoryPosts(category)
	if err != nil {
		return nil, nil, err
	}
	posts = make([]map[string]any, len(numbers))
	if failed, firstErr := forEachConcurrent(len(numbers), fetchConcurrency, func(i int) error {
		p, err := c.postJSON(numbers[i], false)
		if err != nil {
			return fmt.Errorf("記事 %d: %w", numbers[i], err)
		}
		posts[i] = p
		return nil
	}); failed > 0 {
		return nil, nil, fmt.Errorf("%d/%d 件の記事を取得できませんでした: %w", failed, len(numbers), firstErr)
	}
	return numbers, posts, nil
}

// mapSyncFiles は記事（numbers[i] と posts[i] が対）を書き出すファイルへ対応付け、記事どうしの衝突を検査する。
// excluded は検索に当たったがカテゴリが一致しなかった件数（in: は前方一致のため）。skipped は書き出し先の指定が skip の記事。
func mapSyncFiles(category string, numbers []int, posts []map[string]any) (files []syncFile, skipped []syncSkipped, excluded int, err error) {
	var problems []string
	owner := map[string]int{}        // syncPathKey(rel) → 記事番号
	byDirective := map[string]bool{} // syncPathKey(rel) → 書き出し先を指定で決めたか
	for i, p := range posts {
		cat, _ := p["category"].(string)
		name, _ := p["name"].(string)
		cat, name = stdhtml.UnescapeString(cat), stdhtml.UnescapeString(name)
		// カテゴリの判定は記事名・本文の指定と独立に先に行う（指定があっても対象の外の記事は書かない）
		dirParts, inside := syncCategoryParts(category, cat)
		if !inside {
			excluded++ // in: は前方一致なので、Users/me/skills2 のような隣のカテゴリも検索に当たる
			continue
		}
		body := normalizeSyncBody(p["body_md"])
		spec, rest, found, err := findSyncDirective(body)
		// skip はパスの検査・衝突の検査より前で分ける（書かない記事はファイルの鍵を持たない。後ろで分けると空のパスどうしが衝突する）
		if err == nil && found && spec == syncSkipSpec {
			skipped = append(skipped, syncSkipped{number: numbers[i], category: cat, name: name})
			continue
		}
		var rel string
		switch {
		case err != nil:
		case found:
			// 指定のある記事では記事名を書き出し先に使わない（記事名の / の検査もしない。人が読める題には / が普通に入る）
			rel, err = syncDirectiveRelPath(dirParts, cat, spec)
			body = rest
		default:
			rel, err = syncNamedRelPath(dirParts, cat, name)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("記事 #%d: %v", numbers[i], err))
			continue
		}
		key := syncPathKey(rel)
		if prev, dup := owner[key]; dup {
			how := "記事名を変えてください"
			if by := directiveNumbers(byDirective[key], prev, found, numbers[i]); by != "" {
				how = fmt.Sprintf("%s の書き出し先は本文の指定（esa-sync:）による。記事の複製で指定が写っていないか確かめ、指定か記事名を変えてください", by)
			}
			problems = append(problems, fmt.Sprintf("記事 #%d と #%d が同じファイル %s になります（大文字小文字・Unicode の正規化も区別しません。%s）",
				prev, numbers[i], visible(rel), how))
			continue
		}
		owner[key] = numbers[i]
		byDirective[key] = found
		f := syncFile{rel: rel, number: numbers[i], body: body, byDirective: found, catDir: path.Join(dirParts...)}
		if !found {
			if warn := syncSkillWarning(body, rel); warn != "" {
				f.warns = append(f.warns, warn)
			}
		}
		if warn := syncSkipIntentWarning(body); warn != "" {
			f.warns = append(f.warns, warn)
		}
		if by, ok := p["updated_by"].(map[string]any); ok {
			f.updatedBy, _ = by["screen_name"].(string)
		}
		f.updatedAt, _ = p["updated_at"].(string)
		files = append(files, f)
	}
	// ファイルとディレクトリの衝突（記事 a.md と、カテゴリ a.md 配下の記事）。計画の段階で止めないと、
	// 片方を書いた後にもう片方で失敗して、途中まで書いた状態になる。
	for _, f := range files {
		for d := path.Dir(f.rel); d != "."; d = path.Dir(d) {
			if n, clash := owner[syncPathKey(d)]; clash {
				msg := fmt.Sprintf("記事 #%d のファイル %s と、記事 #%d のディレクトリ %s がぶつかります",
					n, visible(d), f.number, visible(d))
				// f のディレクトリ d がカテゴリから来ている（指定の部分でない）なら、f の指定は原因ではない
				fromDirective := f.byDirective && !syncIsSameOrAncestor(d, f.catDir)
				if by := directiveNumbers(byDirective[syncPathKey(d)], n, fromDirective, f.number); by != "" {
					msg += fmt.Sprintf("（%s の書き出し先は本文の指定による）", by)
				}
				problems = append(problems, msg)
			}
		}
	}
	if len(problems) > 0 {
		return nil, nil, 0, fmt.Errorf("ファイルに対応付けられない記事があるため、この対象は 1 件も書き込みません:\n  %s", strings.Join(problems, "\n  "))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].number < skipped[j].number })
	return files, skipped, excluded, nil
}

// syncIsSameOrAncestor は d が p と同じか、p の上位のディレクトリか（syncPathKey で比べる）。
func syncIsSameOrAncestor(d, p string) bool {
	dk, pk := syncPathKey(d), syncPathKey(p)
	return p != "" && (dk == pk || strings.HasPrefix(pk, dk+"/"))
}

// directiveNumbers は、衝突した 2 記事のうち書き出し先を本文の指定で決めたものの番号を「#1」「#1 と #2」の形で返す（無ければ空）。
func directiveNumbers(aBy bool, a int, bBy bool, b int) string {
	switch {
	case aBy && bBy:
		return fmt.Sprintf("#%d と #%d", a, b)
	case aBy:
		return fmt.Sprintf("#%d", a)
	case bBy:
		return fmt.Sprintf("#%d", b)
	}
	return ""
}

// syncPathKey はパスの同一性の鍵。macOS の既定のファイルシステム（APFS）は大文字小文字を区別せず、
// Unicode の正規化（NFC / NFD）も同一視するので、バイト列で比べると別の記事が同じファイルに書かれて片方が消える。
//
// 🚨 strings.ToLower にしない。σ/ς・ß/ss・ﬀ/ff のような組を畳まず、APFS では同じファイルになる 2 記事を
// 別の鍵にする（red team で再現: 1 回目は 1 件だけ書いて失敗し、以後は実行のたびに中身が入れ替わった）。
// Unicode の case folding（cases.Fold）+ NFC は、2026-09-29 に APFS で試した 9 組（上の 3 組・µ/μ・ſ/s・ϐ/β・
// Foo/foo・NFC/NFD・APFS が区別する İ/i）すべてで APFS の同一視と一致した。完全に一致する保証は無い。
//
// Cherokee だけは cases.Fold が小文字（U+AB70〜 / U+13F8〜）へ畳み、APFS は大文字と同じとみなすので、
// 畳んだ後に大文字へそろえる（red team が全コードポイントの候補 1 万 6304 組を APFS で照合して見つけた
// 唯一のずれ。172 組すべて Cherokee）。
// 大文字小文字を区別するボリューム（外付けディスク等）では、別のファイルを同じとみなして拒否する側に倒れる。
func syncPathKey(rel string) string {
	folded := cases.Fold().String(norm.NFC.String(rel))
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cherokee, r) {
			return unicode.ToUpper(r)
		}
		return r
	}, folded)
}

// visible は表示用に、端末で見えない・表示を変える文字をエスケープする（\t はそのまま）。
// 🚨 esa の本文・記事名は他人が書ける。CR や ESC を生で端末へ出すと、dry-run の表示で行を隠して確認を
// すり抜けられる。見えない文字（タグ文字 U+E0000〜・ゼロ幅・異体字セレクタ・埋め字）は、端末では何も
// 表示されないのに Claude には読めるので、書き出し先が Claude の skill なら指示を仕込める。
// 表示だけの変換で、書き出すファイルの中身は変えない。
func visible(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r != '\t' && isInvisibleRune(r) {
			fmt.Fprintf(&sb, "\\x{%x}", r)
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func isInvisibleRune(r rune) bool {
	switch r {
	case '\u2028', '\u2029', // 行・段落の区切り
		'\u115f', '\u1160', '\u3164', '\uffa0': // Hangul の埋め字（幅はあるが何も描かれない）
		return true
	}
	// 絵文字の FE0F / ZWJ もエスケープされて表示が崩れるが、隠し込みの手口（異体字セレクタの連結）と
	// 見分けられないので区別しない（表示だけの変換で、ファイルの中身は変えない）。
	return unicode.IsControl(r) || // C0 / DEL / C1（CR・ESC を含む）
		unicode.Is(unicode.Cf, r) || // 書式文字（タグ文字・ゼロ幅・BOM・ソフトハイフン・双方向の制御）
		unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) // CGJ・未割り当ての無視される範囲（U+E0080〜 等）
}

// syncListAttempts は一覧を取り直す上限。2 回続けて同じ一覧が得られたら確定する。
const syncListAttempts = 4

// listCategoryPosts はカテゴリ配下（前方一致）の記事番号を集める。
//
// 🚨 ページ送りの途中で記事が消える・カテゴリから外れると、後ろのページが前へずれて、残っている記事が
// どのページにも出ないまま「全件」になる（sort を固定しても防げない）。1 回の一覧では検出できないので、
// 2 回続けて同じ集合が得られるまで取り直す。
func (c *client) listCategoryPosts(category string) ([]int, error) {
	var prev []int
	for attempt := 1; attempt <= syncListAttempts; attempt++ {
		cur, err := c.listCategoryPostsOnce(category)
		if err != nil {
			return nil, err
		}
		if attempt > 1 && fmt.Sprint(cur) == fmt.Sprint(prev) {
			return cur, nil
		}
		prev = cur
	}
	return nil, fmt.Errorf("カテゴリ %q の一覧が %d 回取り直しても安定しませんでした（記事の追加・移動が続いている可能性があります）。しばらくしてから再実行してください",
		category, syncListAttempts)
}

func (c *client) listCategoryPostsOnce(category string) ([]int, error) {
	// sort を固定する。既定の並びは更新順で、ページ送りの途中で記事が更新されると取りこぼす。
	query := fmt.Sprintf(`in:"%s" sort:number-asc`, category)
	seen := map[int]bool{}
	var numbers []int
	for page := 1; ; page++ {
		if page > maxSyncPages {
			return nil, fmt.Errorf("カテゴリ %q の記事が %d ページを超えました（一覧を切り詰めて書き出さないため中止します）", category, maxSyncPages)
		}
		results, hasNext, err := c.searchPage(query, page)
		if err != nil {
			return nil, fmt.Errorf("カテゴリ %q の一覧（%d ページ目）: %w", category, page, err)
		}
		added := 0
		for _, r := range results {
			if !seen[r.Number] {
				seen[r.Number] = true
				numbers = append(numbers, r.Number)
				added++
			}
		}
		// added == 0 は保険: 最後より先のページで 1 ページ目が返る形（searchPage のコメント）でも止まる。
		if !hasNext || added == 0 {
			sort.Ints(numbers)
			return numbers, nil
		}
	}
}

// syncRelPath は記事のカテゴリと名前から、dir からの相対パスを作る。
// 記事が root カテゴリの配下でなければ inside=false。
func syncRelPath(root, category, name string) (rel string, inside bool, err error) {
	dirParts, inside := syncCategoryParts(root, category)
	if !inside {
		return "", false, nil
	}
	rel, err = syncNamedRelPath(dirParts, category, name)
	return rel, true, err
}

// syncCategoryParts は記事のカテゴリが対象の配下かを判定し、配下なら対象の dir からのディレクトリの要素を返す
// （要素の検査はしない。検査は syncJoinParts がファイルの要素と一緒に行う）。
func syncCategoryParts(root, category string) (dirParts []string, inside bool) {
	var sub string
	switch {
	case category == root:
	case strings.HasPrefix(category, root+"/"):
		sub = strings.TrimPrefix(category, root+"/")
	default:
		return nil, false
	}
	if sub != "" {
		dirParts = strings.Split(sub, "/")
	}
	return dirParts, true
}

// syncNamedRelPath は今の規則（カテゴリ/記事名 → dir/…/記事名.md）で書き出し先を決める。書き出し先の指定が無い記事だけに使う。
func syncNamedRelPath(dirParts []string, category, name string) (string, error) {
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("記事名 %q に / が含まれています", visible(name))
	}
	file := name
	if !strings.HasSuffix(strings.ToLower(file), ".md") { // SKILL.MD を SKILL.MD.md にしない
		file += ".md"
	}
	return syncJoinParts(append(append([]string(nil), dirParts...), file),
		fmt.Sprintf("カテゴリ %q / 記事名 %q", visible(category), visible(name)))
}

// syncDirectiveRelPath は本文の書き出し先の指定（esa-sync:）で書き出し先を決める。
// 指定は記事のカテゴリに対応するディレクトリからの相対（対象の dir からではない）。
//
// 🚨 dir 相対にしない。validateSyncTargets は dir の入れ子だけを止め、category の入れ子は通すので、dir 相対だと
// 下位カテゴリの記事が上位の対象の最上位（手で置いた skill を含む）を狙え、今の規則より書ける範囲が広がる（issue 010 の red team）。
// カテゴリ相対なら、記事が書けるディレクトリは今の規則と同じで、記事名の代わりにその下の相対パスを選べるだけ。
func syncDirectiveRelPath(dirParts []string, category, spec string) (string, error) {
	label := fmt.Sprintf("カテゴリ %q / 書き出し先の指定 %q", visible(category), visible(spec))
	switch {
	case !strings.HasSuffix(strings.ToLower(spec), ".md"):
		return "", fmt.Errorf("書き出し先の指定は .md で終えてください。書き出さないなら小文字で %s と書いてください（%s）", syncSkipSpec, label)
	case strings.HasPrefix(spec, "/"), strings.Contains(spec, `\`):
		return "", fmt.Errorf("書き出し先の指定は / 区切りの相対パスにしてください（%s）", label)
	}
	parts := strings.Split(spec, "/")
	for _, p := range parts {
		// NBSP・全角空白・ゼロ幅の文字で、正規のパスと見分けにくい別のディレクトリを作らせない
		// （記事名と違い、指定は esa の名前の整形を通らない）。
		if r, _ := utf8.DecodeRuneInString(p); p != "" && isSyncBlankRune(r) {
			return "", fmt.Errorf("書き出し先の指定の要素 %q が空白で始まっています（%s）", visible(p), label)
		}
		if r, _ := utf8.DecodeLastRuneInString(p); p != "" && isSyncBlankRune(r) {
			return "", fmt.Errorf("書き出し先の指定の要素 %q が空白で終わっています（%s）", visible(p), label)
		}
		if strings.IndexFunc(p, isInvisibleRune) >= 0 {
			return "", fmt.Errorf("書き出し先の指定の要素 %q に見えない文字が含まれています（%s）", visible(p), label)
		}
	}
	return syncJoinParts(append(append([]string(nil), dirParts...), parts...), label)
}

// syncJoinParts は dir からのパスの要素を検査して結合する（最後の要素がファイル）。
// 今の規則のパスと書き出し先の指定のパスの両方がここを通る（検査を 2 つ書かない）。
func syncJoinParts(parts []string, label string) (string, error) {
	for i, p := range parts {
		// ファイル名の上限は UTF-16 の単位で 255（APFS で実測: 絵文字 127 個は通り 128 個で失敗、NFD は分解した形で数える）。
		// ファイルは一時ファイルの接尾辞を付けた長さで測る（測らないと、計画は通って書き込みの途中で失敗し、途中まで書いた状態になる）。
		n := p
		if i == len(parts)-1 {
			n += syncTmpSuffix
		}
		if l := len(utf16.Encode([]rune(n))); l > syncNameMax {
			return "", fmt.Errorf("名前 %q が長すぎます（ファイル名の上限は UTF-16 で %d。一時ファイルの分を含めて %d）", visible(p), syncNameMax, l)
		}
		if p == "" || p == "." || p == ".." || p == ".md" || strings.ContainsRune(p, 0) || strings.HasSuffix(syncPathKey(p), syncPathKey(syncTmpSuffix)) {
			return "", fmt.Errorf("パスの要素 %q が不正です（%s）", visible(p), label)
		}
	}
	return path.Join(parts...), nil
}

// syncDirectiveRe は本文の最後の空でない行に書く、書き出し先の指定の厳密な形。
var syncDirectiveRe = regexp.MustCompile(`^<!-- esa-sync: (.+) -->$`)

// extractSyncDirective は正規化済みの本文（normalizeSyncBody の後）から書き出し先の指定を取り出し、
// 指定の行を取り除いた本文を返す。指定が無ければ found=false で本文はそのまま。
//
// 🚨 指定として読むのは最後の空でない行だけで、その行は加工せずに照合する（先頭の空白を削ると、字下げのコード例を指定として食う）。
// 最初の空でない行が指定らしいときもエラーにする（skill の front matter の上に書く間違いが一番起きやすい）。本文の途中は見ない。
// 厳密な形でないのに指定らしい行（全角のコロン・大文字・ゼロ幅の文字・引用など）はエラーにする。黙って今の規則に落とすと、
// 書き出し先が変わったことに気づけず、指定の行も書き出したファイルに残る（issue 010 の red team）。
func extractSyncDirective(body string) (spec, rest string, found bool, err error) {
	lines := strings.Split(body, "\n")
	i := lastNonBlankLine(lines, len(lines))
	if i < 0 {
		return "", body, false, nil
	}
	m := syncDirectiveRe.FindStringSubmatch(lines[i])
	// 最後の行が skip なら、skip らしい別の行（使い方の例）とは意味が食い違わないので止めない（issue 015 の red team）
	sameAsSkip := func(l string) bool { return m != nil && m[1] == syncSkipSpec && looksLikeSkipDirective(l) }
	// 先頭に置く間違いが一番起きやすい（skill の front matter の上に書く）。黙って見逃すと、指定の行が 1 行目に残り、
	// 書き出し先も記事名の規則のままになる。本文の途中（コード例）は触らない。
	if f := firstNonBlankLine(lines); f >= 0 && f != i && looksLikeSyncDirective(lines[f]) && !sameAsSkip(lines[f]) {
		return "", "", false, fmt.Errorf("書き出し先の指定 %q が本文の先頭にあります（本文の最後の行に書いてください）", visible(lines[f]))
	}
	// 🚨 厳密な形の skip が本文の途中にあるのに最後の行が skip でなければ止める。黙って見逃すと、書かないつもりの記事が書き出される
	// （.md の指定と違い、被害は別のファイルの上書き。issue 015 の red team）。コードブロックの中（使い方の例）は見ない。
	if k := syncStraySkipLine(lines, i); k >= 0 && (m == nil || m[1] != syncSkipSpec) {
		return "", "", false, fmt.Errorf("書き出さない指定 %q が本文の途中（%d 行目）にあります（効かせるなら本文の最後の行に書いてください）", visible(lines[k]), k+1)
	}
	if m == nil {
		if looksLikeSkipDirective(lines[i]) {
			return "", "", false, fmt.Errorf("最後の行 %q が書き出さない指定の形になっていません（<!-- esa-sync: %s --> の形で 1 行に書いてください）", visible(lines[i]), syncSkipSpec)
		}
		if looksLikeSyncDirective(lines[i]) {
			return "", "", false, fmt.Errorf("最後の行 %q が書き出し先の指定の形になっていません（<!-- esa-sync: パス --> の形で 1 行に書いてください）", visible(lines[i]))
		}
		return "", body, false, nil
	}
	if j := lastNonBlankLine(lines, i); j >= 0 && looksLikeSyncDirective(lines[j]) && !sameAsSkip(lines[j]) {
		return "", "", false, fmt.Errorf("書き出し先の指定が 2 つあります（%q と %q。1 つにしてください）", visible(lines[j]), visible(lines[i]))
	}
	// HTML のコメントの中に -- は書けない（esa の画面ではそこでコメントが閉じ、残りが本文として出る）。front matter の指定には関係ない
	if strings.Contains(m[1], "--") {
		return "", "", false, fmt.Errorf("書き出し先の指定 %q に -- は使えません（コメントの中に -- は書けません）", visible(m[1]))
	}
	// 指定の直前の空の行（空白・見えない文字だけの行）も、指定を探すときと同じ定義で落とす
	k := lastNonBlankLine(lines, i)
	rest = strings.Join(lines[:k+1], "\n")
	if rest != "" {
		rest += "\n"
	}
	return m[1], rest, true, nil
}

// firstNonBlankLine は lines の中で最初の空でない行の添字を返す（無ければ -1）。
func firstNonBlankLine(lines []string) int {
	for k, l := range lines {
		if strings.IndexFunc(l, func(r rune) bool { return !isSyncBlankRune(r) }) >= 0 {
			return k
		}
	}
	return -1
}

// lastNonBlankLine は lines[:end] の中で最後の空でない行の添字を返す（無ければ -1）。
func lastNonBlankLine(lines []string, end int) int {
	for k := end - 1; k >= 0; k-- {
		if strings.IndexFunc(lines[k], func(r rune) bool { return !isSyncBlankRune(r) }) >= 0 {
			return k
		}
	}
	return -1
}

// isSyncBlankRune は「空の行」を作る文字（空白と見えない文字）。U+200B だけの行の後ろにある指定も見つけるため、見えない文字も数える。
func isSyncBlankRune(r rune) bool {
	return unicode.Is(unicode.White_Space, r) || isInvisibleRune(r)
}

// syncDirectiveLooseRe は崩れた指定を拾うための緩い形（NFKC・見えない文字を除く・小文字にした後の行に当てる）。
// 「esa」「区切り」「sync」「記号」「コロン」「.md で終わるパス」の並び。
//   - 区切り: 空白・ハイフンの類（U+2010〜2015・U+2212・U+2043・U+2E3A・U+30FC の長音）・〜 ~ ・ . / _ \（Markdown のエスケープ）
//   - sync とコロンの間: 空白・` * _ \（コード・強調の記法とエスケープ）
//   - コロン: : と U+2236（全角のコロンは NFKC で : になる）
//
// 🚨 この判定は書き損じに気づかせる補助で、安全の最後の砦ではない。指定が効かなかった記事は、dry-run に記事名のパスの
// 新規ファイルとして出る（「書き出し先は本文の指定による」が付かない）ので、そこで気づける。
// 優先するのは誤検出で対象全体を止めないこと。そのため「コロンの後に .md で終わるパスが続く」まで要件にする
// （「esa sync: カテゴリを同期する」「# esa-sync: 書き出し先の指定」のような説明の文を止めない。issue 010 の red team、3 周目）。
//
// 脅威モデル: 防ぎたいのは、記事を書いた本人が指定を書き損じたときに、黙って記事名の規則に戻ること。悪意のある編集者は対象外
// （指定を書かずとも、記事名で同じ範囲に書ける）。
// 検出しない形（dry-run の書き出し先と差分で気づく）: esa と sync の文字を似た別の文字に置き換えたもの・コロンやパスを落としたもの・
// .md で終わらないパス・1 行に収めずに複数行へ分けたもの・指定の後ろに追記して指定が本文の途中に押し出されたもの。
var syncDirectiveLooseRe = regexp.MustCompile(`(^|[^a-z0-9])esa[\s\-\x{2010}-\x{2015}\x{2212}\x{2043}\x{2e3a}\x{30fc}\x{301c}~\x{30fb}./_\\]*sync[\s` + "`" + `*_\\]*[:\x{2236}]\s*\S*\.md($|[^a-z0-9])`)

// syncSkipLooseRe は崩れた skip の指定を拾う形（syncDirectiveLooseRe と同じ正規化の後に当てる。issue 015）。
//
// .md のパスと違い skip は説明の文に普通に出るので、行が指定だけでできているときに限る: 前後に許すのは空白・コメントの記号
// （<! -- — > と HTML のエンティティ）・コード / 強調の記法・引用符だけ。「…と書くと esa-sync: skip になる」のような文や
// 「# esa-sync: skip を使う」の見出し、skipping は当てない（issue 015 の反証レビュー P1）。
// 崩れた skip を黙って記事名の規則に戻すと、書かないつもりの記事が書き出されて別のファイルを上書きしうる（.md の崩れより被害の向きが悪い）。
// 検出しない形: skip の綴りの崩れ・行の前後に文を付けたもの・YAML の行末コメント付き（dry-run の書き出し先で気づく）。
var syncSkipLooseRe = regexp.MustCompile(`^(?:&lt;|&gt;|[\s<!>\-\x{2010}-\x{2015}*_` + "`" + `\\"'])*esa[\s\-\x{2010}-\x{2015}\x{2212}\x{2043}\x{2e3a}\x{30fc}\x{301c}~\x{30fb}./_\\]*sync[\s` + "`" + `*_\\"']*[:\x{2236}]\s*["']?skip(?:&lt;|&gt;|[\s<!>\-\x{2010}-\x{2015}*_` + "`" + `\\"'])*$`)

// normalizeSyncLine は崩れの検出の前の正規化（NFKC・見えない文字を除く・小文字）。
func normalizeSyncLine(line string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if isInvisibleRune(r) {
			return -1
		}
		return r
	}, norm.NFKC.String(line)))
}

// syncListMarkerRe は箇条書き・番号付きの項目の頭（- * + 1. 1)）。
var syncListMarkerRe = regexp.MustCompile(`^\s*(?:[-*+]|[0-9]+[.)])\s+`)

// looksLikeSkipDirective は、厳密な形でなくても skip の指定を意図したと読める行か（本文のコメントの崩れの検出に使う）。
// 箇条書きの項目は、中身がコメント（<! / &lt;!）で始まるときだけ見る（「- esa-sync: skip」は使い方の説明として止めない。
// 「1. <!-- esa-sync: skip -->」はコメントを書いたつもりの崩れとして止める。issue 015 の red team）。
func looksLikeSkipDirective(line string) bool {
	s := normalizeSyncLine(line)
	if loc := syncListMarkerRe.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
		if !strings.HasPrefix(s, "<!") && !strings.HasPrefix(s, "&lt;!") {
			return false
		}
	}
	return syncSkipLooseRe.MatchString(s)
}

// syncFenceRe はコードブロックのフェンスの行（CommonMark: 字下げは 3 文字まで、` か ~ を 3 つ以上）。
var syncFenceRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// syncStraySkipLine は、最後の行（except）以外で、厳密な形の skip のコメントだけの行を探す（コードブロックの中は除く）。無ければ -1。
//
// フェンスは CommonMark と同じく、開いた記号と同じ種類で同じ長さ以上の、後ろに何も無い行だけを閉じとみなす
// （行ごとに反転すると、入れ子のコード例 ````md の中の ``` で外に出たと数え、コード例の skip で記事を止める。issue 015 の red team 2 周目）。
func syncStraySkipLine(lines []string, except int) int {
	var open string // 開いているフェンスの記号の並び（空なら外）
	for k, l := range lines {
		if m := syncFenceRe.FindStringSubmatch(l); m != nil {
			switch {
			case open == "":
				if !(m[1][0] == '`' && strings.Contains(m[2], "`")) { // info に ` を含む行はフェンスでない
					open = m[1]
				}
			case m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(m[2]) == "":
				open = ""
			}
			continue
		}
		if open != "" || k == except {
			continue
		}
		if m := syncDirectiveRe.FindStringSubmatch(l); m != nil && m[1] == syncSkipSpec {
			return k
		}
	}
	return -1
}

// syncSkipIntentRe は「esa〜sync の直後に、記号と空白だけを挟んで skip の語」がある行（normalizeSyncLine の後に当てる）。結果の側の注意（syncSkipIntentWarning）に使う。
// 間に語を挟ませないのは、「esa sync の実行で skip された記事は…」のような説明の文に注意を出さないため（red team 2 周目）。
// コロンは必須にしない（コロンの抜けた <!-- esa-sync skip --> や esa-sync=skip も拾う。red team 3 周目）。
var syncSkipIntentRe = regexp.MustCompile("(^|[^a-z0-9])esa[\\s\\-_.\\x{2010}-\\x{2015}\\x{2212}\\x{30fc}\\x{301c}~\\\\]*sync[\\s`*_\\\\\"'=:\\x{2236}]+skip($|[^a-z0-9])")

// syncCommentRe は本文の HTML のコメント（複数行を含む）。
var syncCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// syncSkipIntentWarning は、書き出す記事の本文に skip の指定らしい行があるときの注意を返す（無ければ空）。
//
// 🚨 崩れた skip の字面の検出は、塞ぐたびに別の崩し方が出た（issue 015 の red team: 1 行にまとめた metadata・本文の途中・
// 番号付きの項目）。字面では閉じないので、最後の砦は結果の側に置く: 書き出す記事で、本文のどこかに「esa〜sync … skip」の行があれば、
// dry-run と --apply の出力に毎回注意を出す（エラーにはしない。skip の使い方を説明する記事も書き出せるように）。
func syncSkipIntentWarning(body string) string {
	// 複数行に分けたコメント（<!-- esa-sync:\nskip -->）も 1 行につないで見る（red team 2 周目。010 から字面では検出しない形）
	lines := strings.Split(body, "\n")
	for _, c := range syncCommentRe.FindAllString(body, -1) {
		if strings.Contains(c, "\n") {
			lines = append(lines, strings.ReplaceAll(c, "\n", " "))
		}
	}
	for _, l := range lines {
		if syncSkipIntentRe.MatchString(normalizeSyncLine(l)) {
			return fmt.Sprintf("本文に書き出さない指定らしい行 %q がありますが、指定として効いていないため書き出します"+
				"（書き出さないなら、本文の最後の行を <!-- esa-sync: %[2]s --> だけにするか、front matter の metadata に esa-sync: %[2]s を書いてください）",
				visible(l), syncSkipSpec)
		}
	}
	return ""
}

// looksLikeSyncDirective は、厳密な形でなくても書き出し先の指定を意図したと読める行か。
func looksLikeSyncDirective(line string) bool {
	return syncDirectiveLooseRe.MatchString(normalizeSyncLine(line)) || looksLikeSkipDirective(line)
}

// normalizeSyncBody は本文の改行を LF にそろえ、空でなければ末尾を改行で終える。
func normalizeSyncBody(v any) string {
	s, _ := v.(string)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// openSyncRoot は書き出し先を os.Root として開く。dir が無ければ nil（全件が新規として扱われる）。
//
// 🚨 書き込みは必ずこの Root を通す。os.Root はシンボリックリンクや .. で dir の外へ出る操作を
// 拒否するので、syncRelPath の検査をすり抜けた記事名があっても dir の外は書けない。
func openSyncRoot(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("書き出し先 %s を開けません: %w", dir, err)
	}
	return root, nil
}

// syncDest は書き出し先の 1 ファイルの今の状態。
type syncDest struct {
	content string
	exists  bool
	perm    fs.FileMode // exists のときだけ意味を持つ
}

// readSyncDest は書き出し先の今の状態を読む。通常のファイル以外（リンク・ディレクトリ）と、途中の要素が
// ディレクトリでない形はエラー。書き出し先の検査はここだけに置く（計画・書く前・置き換える直前が同じ検査を通る）。
func readSyncDest(root *os.Root, rel string) (syncDest, error) {
	if root == nil {
		return syncDest{}, nil
	}
	// 途中の要素がリンクだと、別の記事のディレクトリへ書き込む形になる（dir の中を指すリンクは Root が通す）。
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		fi, err := root.Lstat(filepath.FromSlash(d))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return syncDest{}, err
		}
		if !fi.IsDir() {
			return syncDest{}, fmt.Errorf("途中の %s がディレクトリではありません（%s）", visible(d), fi.Mode().Type())
		}
	}
	name := filepath.FromSlash(rel)
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return syncDest{}, nil
	}
	if err != nil {
		return syncDest{}, err
	}
	if !fi.Mode().IsRegular() {
		return syncDest{}, fmt.Errorf("通常のファイルではありません（%s）", fi.Mode().Type())
	}
	b, err := root.ReadFile(name)
	if err != nil {
		return syncDest{}, err
	}
	return syncDest{content: string(b), exists: true, perm: fi.Mode().Perm()}, nil
}

// syncNameMax は 1 つのファイル名の長さの上限（UTF-16 の単位。APFS）。
const syncNameMax = 255

// syncTmpSuffix は書き込みの一時ファイルの接尾辞。記事のパスの要素には使わせない（syncRelPath）。
const syncTmpSuffix = ".esa-sync-tmp"

// writeSyncFile は root 配下へ一時ファイル経由で書き、rename で置き換える。既存ファイルの権限は保つ。
//
// 🚨 置き換える直前に書き出し先を読み直し、計画のとき（差分を表示したとき）の内容と一致しなければ書かない。
// 一致を見ずに書くと、計画から書き込みまでの間の手編集が、差分に一度も出ないまま消える。
//
// 読み直しから rename までの間（比較 1 回ぶん）の手編集は防げない（比較と置き換えを不可分にする手段が無い）。
// 呼び出し側は acquireSyncLock を持ち、前回の一時ファイルを消しておくこと（applySyncPlan）。
func writeSyncFile(root *os.Root, f syncFile, expected string, expectExists bool) error {
	name := filepath.FromSlash(f.rel)
	// 書く前にも 1 度確かめる（ディレクトリを作ってから食い違いに気づくと、空のディレクトリが残る）。
	cur, err := checkSyncDestUnchanged(root, f.rel, expected, expectExists)
	if err != nil {
		return err
	}
	if d := filepath.Dir(name); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	perm := fs.FileMode(0o644)
	if cur.exists {
		perm = cur.perm
	}
	// O_TRUNC で開き直さないのは、一時ファイルの名前がリンクだった場合に、リンク先を書き換えないため。
	tmp := name + syncTmpSuffix
	fh, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, werr := io.WriteString(fh, f.body)
	if werr == nil {
		werr = fh.Sync()
	}
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		_, werr = checkSyncDestUnchanged(root, f.rel, expected, expectExists)
	}
	if werr == nil {
		werr = root.Rename(tmp, name)
	}
	if werr != nil {
		_ = root.Remove(tmp)
	}
	return werr
}

// checkSyncDestUnchanged は書き出し先が計画のとき（差分を表示したとき）のままかを確かめ、今の状態を返す。
func checkSyncDestUnchanged(root *os.Root, rel, expected string, expectExists bool) (syncDest, error) {
	cur, err := readSyncDest(root, rel)
	if err != nil {
		return syncDest{}, err
	}
	if cur.exists != expectExists || cur.content != expected {
		return syncDest{}, errors.New("差分を表示した後に書き出し先が変わりました。もう一度差分を確認してください")
	}
	return cur, nil
}
