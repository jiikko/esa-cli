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
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const syncHelp = `esa sync - esa のカテゴリ配下の記事を、ローカルのディレクトリへ書き出す（esa → ローカルの一方向）

使い方:
  esa sync [オプション] [名前...]   sync.yml の対象の差分を表示する（既定は dry-run。書き込まない）
  esa sync --apply [名前...]       差分を表示してから書き込む（書くのはその時点の esa の内容。
                                   dry-run の後に esa 側が変わっていれば、変わった内容が書かれる）
  esa sync add                     対象を対話式で追加する（esa sync add --help）
  esa sync list                    登録済みの対象を一覧する
  esa sync help                    このヘルプ

  名前を省くと全対象。フラグは名前の前後どちらに書いてもよい。

オプション:
  -apply   書き込む（付けなければ差分の表示だけ）
  （共通オプション -team/-profile は esa --help を参照）

対応の規則（category: Users/me/skills、dir: ~/.claude/skills の場合）:
  Users/me/skills/foo/SKILL   → ~/.claude/skills/foo/SKILL.md
  Users/me/skills/README      → ~/.claude/skills/README.md
  Users/me/skills/foo/run.md  → ~/.claude/skills/foo/run.md   （.md で終わる名前はそのまま）
  中身は記事本文の Markdown（front matter は付けない。改行は LF にそろえる）。WIP の記事も対象。

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

設定ファイル: $XDG_CONFIG_HOME/esa-cli/sync.yml（未設定なら ~/.config/esa-cli/sync.yml）
  targets:
    - name: skills
      category: Users/me/skills
      dir: ~/.claude/skills

例:
  esa sync add
  esa sync                 # 全対象の差分を確認
  esa sync skills --apply  # 1 対象だけ書き込む
`

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

func syncList(args []string) error {
	fs := newFlagSet("sync list")
	positional, done, err := parsePositionals(fs, syncHelp, args)
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
	rel       string // dir からの相対パス（/ 区切り）
	number    int
	body      string
	updatedBy string
	updatedAt string
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
	leftovers []string // 前回中断したときの一時ファイル（計画に載っているファイルの分だけ。走査はしない）
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
	files, excluded, err := mapSyncFiles(t.Category, numbers, posts)
	if err != nil {
		return syncPlan{}, err
	}
	if len(files) == 0 && excluded > 0 {
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
	}
	fmt.Fprintf(w, "  記事 %d 件: 新規 %d / 変更 %d / 変更なし %d\n", len(plan.items), nNew, nChanged, nSame)
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
	if failed, firstErr := forEachConcurrent(len(numbers), 6, func(i int) error {
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
// excluded は検索に当たったがカテゴリが一致しなかった件数（in: は前方一致のため）。
func mapSyncFiles(category string, numbers []int, posts []map[string]any) (files []syncFile, excluded int, err error) {
	var problems []string
	owner := map[string]int{} // syncPathKey(rel) → 記事番号
	for i, p := range posts {
		cat, _ := p["category"].(string)
		name, _ := p["name"].(string)
		rel, inside, err := syncRelPath(category, stdhtml.UnescapeString(cat), stdhtml.UnescapeString(name))
		if !inside {
			excluded++ // in: は前方一致なので、Users/me/skills2 のような隣のカテゴリも検索に当たる
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("記事 #%d: %v", numbers[i], err))
			continue
		}
		key := syncPathKey(rel)
		if prev, dup := owner[key]; dup {
			problems = append(problems, fmt.Sprintf("記事 #%d と #%d が同じファイル %s になります（大文字小文字・Unicode の正規化も区別しません。記事名を変えてください）",
				prev, numbers[i], visible(rel)))
			continue
		}
		owner[key] = numbers[i]
		f := syncFile{rel: rel, number: numbers[i], body: normalizeSyncBody(p["body_md"])}
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
				problems = append(problems, fmt.Sprintf("記事 #%d のファイル %s と、記事 #%d のディレクトリ %s がぶつかります",
					n, visible(d), f.number, visible(d)))
			}
		}
	}
	if len(problems) > 0 {
		return nil, 0, fmt.Errorf("ファイルに対応付けられない記事があるため、この対象は 1 件も書き込みません:\n  %s", strings.Join(problems, "\n  "))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, excluded, nil
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
	var sub string
	switch {
	case category == root:
	case strings.HasPrefix(category, root+"/"):
		sub = strings.TrimPrefix(category, root+"/")
	default:
		return "", false, nil
	}
	var parts []string
	if sub != "" {
		parts = strings.Split(sub, "/")
	}
	if strings.Contains(name, "/") {
		return "", true, fmt.Errorf("記事名 %q に / が含まれています", visible(name))
	}
	file := name
	if !strings.HasSuffix(strings.ToLower(file), ".md") { // SKILL.MD を SKILL.MD.md にしない
		file += ".md"
	}
	parts = append(parts, file)
	for i, p := range parts {
		// ファイル名の上限は UTF-16 の単位で 255（APFS で実測: 絵文字 127 個は通り 128 個で失敗、NFD は分解した形で数える）。
		// ファイルは一時ファイルの接尾辞を付けた長さで測る（測らないと、計画は通って書き込みの途中で失敗し、途中まで書いた状態になる）。
		n := p
		if i == len(parts)-1 {
			n += syncTmpSuffix
		}
		if l := len(utf16.Encode([]rune(n))); l > syncNameMax {
			return "", true, fmt.Errorf("名前 %q が長すぎます（ファイル名の上限は UTF-16 で %d。一時ファイルの分を含めて %d）", visible(p), syncNameMax, l)
		}
		if p == "" || p == "." || p == ".." || p == ".md" || strings.ContainsRune(p, 0) || strings.HasSuffix(syncPathKey(p), syncPathKey(syncTmpSuffix)) {
			return "", true, fmt.Errorf("パスの要素 %q が不正です（カテゴリ %q / 記事名 %q）", visible(p), visible(category), visible(name))
		}
	}
	return path.Join(parts...), true, nil
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
