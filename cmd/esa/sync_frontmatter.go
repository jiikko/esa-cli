package main

import (
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

// 書き出し先の指定を skill の front matter の metadata.esa-sync に書く形（issue 013）。
//
// Claude Code の skill の仕様では metadata は「自前のツールが読む任意の map」で、Claude Code は中身を解釈しない
// （https://code.claude.com/docs/en/skills.md）。仕様に無い最上位のキーは Claude Code では無視されるが、
// claude.ai へのアップロードと Skills API では hard error になるので、metadata の外には置かせない。
//
// front matter は本文の一部なので、末尾のコメントの指定（issue 010）と違って取り除かずに書き出す。

// syncFrontMatterKey は metadata の下に書く、書き出し先の指定のキー。
const syncFrontMatterKey = "esa-sync"

// findSyncDirective は本文から書き出し先の指定を探す（front matter の metadata.esa-sync と、末尾のコメント）。
// rest は書き出す本文（front matter の指定なら本文のまま、コメントの指定ならその行を除いたもの）。
func findSyncDirective(body string) (spec, rest string, found bool, err error) {
	fmSpec, fmFound, err := extractFrontMatterDirective(body)
	if err != nil {
		return "", "", false, err
	}
	spec, rest, found, err = extractSyncDirective(body)
	if err != nil {
		return "", "", false, err
	}
	if fmFound && found {
		return "", "", false, fmt.Errorf("書き出し先の指定が front matter の metadata.%s（%q）と最後の行のコメント（%q）の 2 つにあります（1 つにしてください）",
			syncFrontMatterKey, visible(fmSpec), visible(spec))
	}
	if fmFound {
		return fmSpec, body, true, nil
	}
	return spec, rest, found, nil
}

// syncFrontMatter は本文の先頭の front matter（1 行目の --- から次の --- の行まで）の中身を返す。
// Claude Code と同じく、1 行目がちょうど --- のときだけ front matter とみなす（本文は normalizeSyncBody で LF にそろえた後）。
func syncFrontMatter(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return strings.Join(lines[1:i], "\n"), true
		}
	}
	return "", false
}

// extractFrontMatterDirective は front matter の metadata.esa-sync を読む。無ければ found=false。
//
// 🚨 書き損じは黙って記事名の規則に戻さずエラーにする（issue 010 と同じ方針。戻すと書き出し先が変わったことに気づけない）:
// metadata の外の指定らしいキー、文字列でない値、キーの重複、閉じの無い・1 行目が崩れた・壊れた YAML の front matter の中の指定らしい行。
//
// 「指定らしい」の判定は 2 つ（issue 013 の red team 1〜2 周目）:
//   - YAML として読めたとき: キーが区切りを除いてちょうど esasync で（syncKeyLike）、値が .md で終わるもの
//   - 読めない・閉じの無いとき: 行の中に esa〜sync の並びと .md があるもの（firstSyncKeyLine）。閉じの無いときは最初の空行までしか見ない
//
// 値の .md を条件に入れるのは、水平線の --- で始まる普通の記事や説明の文を止めないため
// （010 の崩れの検出 syncDirectiveLooseRe も「コロンの後に .md のパス」を条件にしている）。
//
// 🚨 字面の検出は書き損じに気づかせる補助で、全部の崩し方は拾わない（拾おうとすると迂回が出るたびに規則が増え、誤検出も増える）。
// 最後の砦は結果の側の警告（syncSkillWarning）: skill の front matter があるのに指定が無く SKILL.md 以外へ書く記事は、
// dry-run と --apply の出力に必ず注意が出る。
//
// 検出しない形（上の警告で気づく）: 複合キー（? [esa-sync]）・語が崩れたキー（esa2sync・esa-syncs）・値がシーケンスのもの・
// metadata の子が 1 つだけでコロンの後の空白を落としたもの（metadata の値が 1 つの文字列になる）。
// merge key（<<: *m）で metadata へ入れた指定は、アンカーの定義の側の esa-sync として止める（誤検出。受容）。
func extractFrontMatterDirective(body string) (spec string, found bool, err error) {
	fm, ok := syncFrontMatter(body)
	if !ok {
		// 1 行目が崩れた（BOM・後ろの空白）か、閉じが無い front matter。Claude Code も front matter として読まないはずで、
		// 中の指定が黙って効かなくなるのを止める。
		first, _, _ := strings.Cut(strings.TrimPrefix(body, "\ufeff"), "\n")
		if strings.TrimRight(first, " \t") == "---" {
			what := "front matter の閉じの --- がありません"
			if !strings.HasPrefix(body, "---\n") {
				what = "front matter の 1 行目は --- だけにしてください（BOM・後ろの空白を入れない）"
			}
			if l, hit := firstSyncKeyLine(untilBlankLine(strings.Split(body, "\n")[1:])); hit {
				return "", false, fmt.Errorf("%s（中に書き出し先の指定 %q らしい行があるため止めます）", what, visible(l))
			}
		}
		return "", false, nil
	}
	docs, err := decodeSyncYAML(fm)
	if err != nil || len(docs) > 1 {
		// 壊れた YAML は Claude Code も「フィールドなし」で読む。文書が 2 つ（中に --- の行）なら 2 つ目以降は読まれない
		why := "YAML が読めません"
		if err == nil {
			why = "中に --- の行があり、YAML の文書が 2 つ以上になっています"
		} else {
			why = fmt.Sprintf("YAML が読めません（%v）", err)
		}
		if l, hit := firstSyncKeyLine(strings.Split(fm, "\n")); hit {
			return "", false, fmt.Errorf("front matter の %s。中に書き出し先の指定 %q らしい行があるため止めます", why, visible(l))
		}
		return "", false, nil
	}
	var specs []*yaml.Node
	var problems []string
	for _, doc := range docs {
		walkSyncYAML(doc, nil, func(p []string, k, v *yaml.Node) {
			if !syncKeyLike(k.Value) {
				return
			}
			v = resolveSyncAlias(v)
			if len(p) == 1 && p[0] == "metadata" && k.Value == syncFrontMatterKey {
				specs = append(specs, v)
				return
			}
			if v.Kind == yaml.ScalarNode && strings.HasSuffix(strings.ToLower(v.Value), ".md") {
				problems = append(problems, visible(strings.Join(append(append([]string(nil), p...), k.Value), ".")))
			}
		})
	}
	switch {
	case len(problems) > 0:
		return "", false, fmt.Errorf("front matter の %s は書き出し先の指定として読みません（metadata の下に %s: パス の形で書いてください）",
			strings.Join(problems, "・"), syncFrontMatterKey)
	case len(specs) == 0:
		return "", false, nil
	case len(specs) > 1:
		return "", false, fmt.Errorf("front matter に metadata.%s が %d 個あります（1 つにしてください）", syncFrontMatterKey, len(specs))
	}
	v := specs[0]
	if v.Kind != yaml.ScalarNode || v.Tag != "!!str" || v.Value == "" {
		return "", false, errors.New("front matter の metadata." + syncFrontMatterKey + " は、書き出し先のパスの文字列にしてください")
	}
	return v.Value, true, nil
}

// decodeSyncYAML は front matter の中身を YAML の文書の並びとして読む（空の文書は数えない）。
func decodeSyncYAML(fm string) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(fm))
	var docs []*yaml.Node
	for {
		var d yaml.Node
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if !isEmptyYAMLDoc(&d) {
			docs = append(docs, &d)
		}
	}
}

// resolveSyncAlias はエイリアス（*a）ならアンカーの値を返す（Claude Code の YAML の読み方と同じ）。
func resolveSyncAlias(v *yaml.Node) *yaml.Node {
	for v.Kind == yaml.AliasNode && v.Alias != nil {
		v = v.Alias
	}
	return v
}

// walkSyncYAML はマッピングのキーと値を、そこまでのキーの並び（シーケンスの要素は "[]"）と一緒に visit へ渡す。
// エイリアス（*a）の先へは降りない（循環を避ける。アンカーの定義の側を通るので、中の指定らしいキーは見落とさない）。
func walkSyncYAML(n *yaml.Node, p []string, visit func(p []string, k, v *yaml.Node)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkSyncYAML(c, p, visit)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			visit(p, k, v)
			walkSyncYAML(v, append(append([]string(nil), p...), k.Value), visit)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkSyncYAML(c, append(append([]string(nil), p...), "[]"), visit)
		}
	}
}

// syncKeyLike は、キーが区切り（- _ . 空白）を除いて小文字にするとちょうど esasync か
// （esa-sync・esa_sync・esaSync・ESA sync・esa.sync）。「詳しくは esa sync」や uses_async は当たらない。
func syncKeyLike(key string) bool {
	k := strings.ToLower(strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || unicode.IsSpace(r) || isInvisibleRune(r) {
			return -1
		}
		return r
	}, norm.NFKC.String(key)))
	return k == "esasync"
}

// syncLineRe は、行の中の「esa〜sync の直後にコロン」（区切りは - _ . 空白、キーの引用符も可）。NFKC（全角のコロンは : になる）・小文字の後に当てる。
// 「esa sync の使い方: README.md」のような説明の文は、esa sync の直後がコロンでないので当たらない（issue 013 の red team 3 周目）。
var syncLineRe = regexp.MustCompile(`(^|[^a-z0-9])esa[\s\-_.]*sync["'\s]*:`)

// firstSyncKeyLine は、esa〜sync のキーと .md を含む行を探す（YAML として読めないときの補助。行末のコメント・フロー形式・
// 空白入りのパス・コロンの後の空白の抜けも拾うため、行全体が「キー: 値」の形であることまでは求めない）。
func firstSyncKeyLine(lines []string) (string, bool) {
	for _, l := range lines {
		n := strings.ToLower(norm.NFKC.String(l))
		if strings.HasPrefix(strings.TrimSpace(n), "<!--") {
			continue // 末尾のコメントの指定（issue 010）とその崩れは extractSyncDirective が見る
		}
		if syncLineRe.MatchString(n) && strings.Contains(n, ".md") {
			return l, true
		}
	}
	return "", false
}

// untilBlankLine は最初の空行の手前までを返す（閉じの無い front matter の範囲。水平線で始まる記事の本文を見ない）。
func untilBlankLine(lines []string) []string {
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			return lines[:i]
		}
	}
	return lines
}

// syncSkillWarning は、skill の front matter（description があるもの）があるのに書き出し先の指定が無く、
// <ディレクトリ>/SKILL.md 以外へ書く記事への注意を返す（無ければ空）。字面の検出が拾わない書き損じを、書き出し先という結果で知らせる。
//
// name は見ない（Claude Code の skill では省ける）。カテゴリ直下の SKILL.md（ディレクトリの無いもの）は Claude Code が skill として読まないので注意を出す。
// skill でないのに description を持つファイル（.claude/agents/*.md など）にも出るので、文言で消し方（自分のパスを指定に書く）を案内する。
// 検出しない形: YAML が読めない front matter（字面の検出も外れる書き損じが重なったときだけ黙る）。
func syncSkillWarning(body, rel string) string {
	if path.Base(rel) == "SKILL.md" && path.Dir(rel) != "." {
		return ""
	}
	fm, ok := syncFrontMatter(body)
	if !ok {
		return ""
	}
	docs, err := decodeSyncYAML(fm)
	if err != nil || len(docs) != 1 || len(docs[0].Content) != 1 || docs[0].Content[0].Kind != yaml.MappingNode {
		return ""
	}
	top := docs[0].Content[0]
	if mappingValue(top, "description") == nil {
		return ""
	}
	return fmt.Sprintf("skill の front matter（description）があるのに書き出し先の指定が無いため、記事名のパスへ書きます。"+
		"skill にするなら front matter の metadata に %[1]s: <skill 名>/SKILL.md を書いてください"+
		"（skill でないファイルなら、metadata に %[1]s: <今の書き出し先のパス> を書くとこの注意は出なくなります）", syncFrontMatterKey)
}
