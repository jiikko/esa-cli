package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// syncTarget は sync.yml の 1 件。esa のカテゴリ配下の記事を dir 配下へ書き出す。
type syncTarget struct {
	Name     string `yaml:"name"`     // esa sync <name> で指定する名前
	Category string `yaml:"category"` // esa のカテゴリ（例: Users/me/skills）
	Dir      string `yaml:"dir"`      // 書き出し先（~ 始まり可。書いたとおりに保存する）
}

type syncConfigFile struct {
	Targets []syncTarget `yaml:"targets"`
}

// syncConfigPath は sync.yml のパスを返す（config.yml と同じディレクトリ）。
//
// config.yml と分けているのは、saveFileConfig が config.yml を struct から組み立て直して書き戻し、
// 手で書いたコメントや未知のキーを消すため。sync の定義は手でも編集する前提なので同居させない。
func syncConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sync.yml"), nil
}

var syncNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// syncReservedNames は esa sync のサブコマンド名。target の名前にすると区別できない。
var syncReservedNames = map[string]bool{"add": true, "list": true, "help": true}

// normalizeCategory はカテゴリの前後の空白と / を落とす。
func normalizeCategory(c string) string {
	return strings.Trim(strings.TrimSpace(c), "/")
}

// expandDir は ~ を展開し、絶対パスでなければ拒否する（cwd に依存させない）。
func expandDir(dir string) (string, error) {
	d := strings.TrimSpace(dir)
	if d == "~" || strings.HasPrefix(d, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(home, strings.TrimPrefix(d, "~"))
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("dir %q は絶対パスか ~ 始まりで指定してください（カレントディレクトリに依存させないため）", dir)
	}
	return filepath.Clean(d), nil
}

// parseSyncConfig は sync.yml の内容を読み、各 target を検証して返す。
// 未知のキー（dir の書き間違い等）は黙って無視せずエラーにする。
func parseSyncConfig(data []byte) ([]syncTarget, error) {
	var cfg syncConfigFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// `---` で区切った 2 つ目以降の文書は読まれない。黙って無視すると、書いたはずの対象が無いことになる
	// （追記の書き戻しでも消える）。
	// 末尾の `---` だけの空の文書は許す（中身の無い文書は Kind が 0 か、null の scalar だけになる）。
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || !isEmptyYAMLDoc(&extra) {
			return nil, errors.New("文書が複数あります（--- で区切らず、1 つの targets: にまとめてください）")
		}
	}
	if err := validateSyncTargets(cfg.Targets); err != nil {
		return nil, err
	}
	return cfg.Targets, nil
}

// validateSyncTargets は対象の一覧を検証する（各項目と、対象どうしの名前・dir の重なり）。カテゴリはその場で正規化する。
// sync が読むときと、esa sync add が保存の前に確かめるときの両方がこれを通る（検証を 2 つに分けない）。
func validateSyncTargets(targets []syncTarget) error {
	seen := map[string]bool{}
	dirs := map[string]string{} // 展開後の dir → name
	for i := range targets {
		t := &targets[i]
		t.Category = normalizeCategory(t.Category)
		if err := validateSyncTarget(*t, seen); err != nil {
			return fmt.Errorf("targets[%d]: %w", i, err)
		}
		seen[t.Name] = true
		// dir が同じか入れ子の 2 対象は、互いの書いたファイルを上書きしうるうえ、dry-run の差分も実際の結果と食い違う。
		dir, _ := expandDir(t.Dir)
		for other, name := range dirs {
			if key, okey := syncPathKey(dir), syncPathKey(other); key == okey || strings.HasPrefix(key, okey+"/") || strings.HasPrefix(okey, key+"/") {
				return fmt.Errorf("targets[%d]: dir %s が %q の dir %s と同じか入れ子です（別々のディレクトリにしてください）", i, dir, name, other)
			}
		}
		dirs[dir] = t.Name
	}
	return nil
}

func isEmptyYAMLDoc(n *yaml.Node) bool {
	if n.Kind == 0 {
		return true
	}
	return n.Kind == yaml.DocumentNode && len(n.Content) == 1 &&
		n.Content[0].Kind == yaml.ScalarNode && n.Content[0].Tag == "!!null" && n.Content[0].Value == ""
}

func validateSyncTarget(t syncTarget, existing map[string]bool) error {
	switch {
	case !syncNameRe.MatchString(t.Name):
		return fmt.Errorf("name %q が不正です（英数字・. _ - のみ、先頭は英数字）", t.Name)
	case syncReservedNames[t.Name]:
		return fmt.Errorf("name %q は esa sync のサブコマンド名なので使えません", t.Name)
	case existing[t.Name]:
		return fmt.Errorf("name %q が重複しています", t.Name)
	case t.Category == "":
		return errors.New("category が空です")
	case strings.Contains(t.Category, `"`):
		return fmt.Errorf("category %q に \" は使えません（検索クエリを組めないため）", t.Category)
	}
	if t.Dir == "" {
		return errors.New("dir が空です")
	}
	_, err := expandDir(t.Dir)
	return err
}

// loadSyncTargets は sync.yml を読む。無ければ空。
func loadSyncTargets() ([]syncTarget, string, error) {
	path, err := syncConfigPath()
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	targets, err := parseSyncConfig(data)
	if err != nil {
		return nil, path, fmt.Errorf("%s の解析に失敗しました: %w", path, err)
	}
	return targets, path, nil
}

const syncConfigHeader = "# esa sync の対象（esa sync add で追加できる。手で編集してもよい）\n" +
	"# category: esa のカテゴリ / dir: 書き出し先（~ 始まり可）\n"

// appendSyncTarget は sync.yml に target を 1 件足す。既存のコメントと並びは残す。
//
// 🚨 足した結果を parseSyncConfig（sync が読むときと同じ検証）に通してから書く。
// ウィザード側で別に検証すると、2 つの検証が食い違ったときに「保存できたのに sync で読めない」になる。
func appendSyncTarget(path string, t syncTarget) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// 追記の前に、今のファイルを sync と同じ検証に通す（複数文書など、書き戻すと消える形を先に止める）。
	if _, err := parseSyncConfig(data); err != nil {
		return fmt.Errorf("%s を読めないため書き込みを中止しました（直してから再実行してください）: %w", path, err)
	}
	// sync.yml がリンク（dotfiles の実体を指す等）なら実体へ書く。リンクごと rename で置き換えると、以後は別のファイルになる。
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%s のリンク先を解決できません: %w", path, err)
		}
		path = real
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s を読めないため書き込みを中止しました（直すか削除してから再実行してください）: %w", path, err)
		}
	}
	if doc.Kind == 0 || isEmptyYAMLDoc(&doc) {
		// コメントだけのファイルなら、そのコメントを残す（既定のヘッダに置き換えない）。
		head := strings.TrimSuffix(syncConfigHeader, "\n")
		if c := strings.TrimSpace(string(data)); c != "" {
			head = c
		}
		doc = yaml.Node{Kind: yaml.DocumentNode, HeadComment: head, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s の最上位が targets: を持つマッピングではないため書き込みを中止しました", path)
	}
	top := doc.Content[0]
	var seq *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "targets" {
			seq = top.Content[i+1]
		}
	}
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode}
		top.Content = append(top.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "targets"}, seq)
	}
	if seq.Kind == yaml.ScalarNode && seq.Tag == "!!null" { // `targets:` だけ書いてある
		*seq = yaml.Node{Kind: yaml.SequenceNode, HeadComment: seq.HeadComment, LineComment: seq.LineComment}
	}
	if seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s の targets がリストではないため書き込みを中止しました", path)
	}
	var item yaml.Node
	if err := item.Encode(t); err != nil {
		return err
	}
	seq.Content = append(seq.Content, &item)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2) // 既定の 4 だと、手で書く 2 桁の YAML と混ざる
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	out := buf.Bytes()
	if _, err := parseSyncConfig(out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, out, 0o600)
}

// writeFileAtomic は同じディレクトリの一時ファイルに書いてから rename する（途中で落ちても半端な内容を残さない）。
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, perm)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}
