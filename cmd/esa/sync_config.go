package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// syncTarget は config.yml の sync: の 1 件。esa のカテゴリ配下の記事を dir 配下へ書き出す。
type syncTarget struct {
	Name     string `yaml:"name"`     // esa sync <name> で指定する名前
	Category string `yaml:"category"` // esa のカテゴリ（例: Users/me/skills）
	Dir      string `yaml:"dir"`      // 書き出し先（~ 始まり可。書いたとおりに保存する）
}

// legacySyncConfigPath は v0.1.8 まで対象を置いていた sync.yml のパス（config.yml と同じディレクトリ）。
// issue 011 で config.yml の sync: に移した。残っていたら読まずに、移すよう案内してエラーにする
// （両方を読むと、どちらが正本か分からなくなる）。
func legacySyncConfigPath() (string, error) {
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

// parseSyncConfig は config.yml の内容から sync: の対象を読み、各 target を検証して返す。
// sync: の各項目の未知のキー（dir の書き間違い等）は黙って無視せずエラーにする。最上位の他のキー（profile / team 等）は見ない。
func parseSyncConfig(data []byte) ([]syncTarget, error) {
	doc, err := parseConfigDoc(data)
	if err != nil {
		return nil, err
	}
	node := mappingValue(doc.Content[0], "sync")
	var targets []syncTarget
	switch {
	case node == nil, node.Kind == yaml.ScalarNode && node.Tag == "!!null": // sync: が無い・`sync:` だけ
	case node.Kind != yaml.SequenceNode:
		return nil, errors.New("sync がリストではありません（- name: … の並びで書いてください）")
	default:
		// 項目の未知のキーを拒むため、sync: の部分だけを書き出して KnownFields で読み直す（yaml.Node.Decode は KnownFields を持たない）
		var buf bytes.Buffer
		if err := yaml.NewEncoder(&buf).Encode(node); err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(&buf)
		dec.KnownFields(true)
		if err := dec.Decode(&targets); err != nil {
			return nil, fmt.Errorf("sync: %w", err)
		}
	}
	if err := validateSyncTargets(targets); err != nil {
		return nil, err
	}
	return targets, nil
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

// loadSyncTargets は config.yml の sync: を読む。無ければ空。
func loadSyncTargets() ([]syncTarget, string, error) {
	path, err := configFilePath()
	if err != nil {
		return nil, "", err
	}
	if legacy, err := legacySyncConfigPath(); err == nil {
		if _, err := os.Lstat(legacy); err == nil {
			return nil, path, fmt.Errorf("%s は使わなくなりました。中身の targets: の下の項目を、%s の sync: の下へ移してから %s を消してください（両方を読むと、どちらが正本か分からなくなるため）",
				legacy, path, legacy)
		}
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

// appendSyncTarget は config.yml の sync: に target を 1 件足す。他のキー（profile / team）・既存のコメント・並びは残す。
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
	doc, err := parseConfigDoc(data)
	if err != nil {
		return fmt.Errorf("%s を読めないため書き込みを中止しました（直してから再実行してください）: %w", path, err)
	}
	top := doc.Content[0]
	seq := mappingValue(top, "sync")
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode}
		top.Content = append(top.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "sync"}, seq)
	}
	if seq.Kind == yaml.ScalarNode && seq.Tag == "!!null" { // `sync:` だけ書いてある
		*seq = yaml.Node{Kind: yaml.SequenceNode, HeadComment: seq.HeadComment, LineComment: seq.LineComment}
	}
	if seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s の sync がリストではないため書き込みを中止しました", path)
	}
	var item yaml.Node
	if err := item.Encode(t); err != nil {
		return err
	}
	seq.Content = append(seq.Content, &item)
	return writeConfigDoc(path, doc, func(out []byte) error {
		_, err := parseSyncConfig(out)
		return err
	})
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
