package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// config.yml の読み書きの共通部分（issue 011）。
//
// 🚨 config.yml は esa config set / init / setup（profile・team）と esa sync add（sync:）の両方が書く。
// どちらも「読んだ YAML のノードのうち、自分のキーだけを書き換えて書き戻す」形にし、他のキー・手で書いたコメントを残す。
// struct から組み立て直して書くと、もう片方のキーを黙って消す（issue 011 の前は config.yml と sync.yml に分けて避けていた）。

const configFileHeader = "# esa-cli 設定ファイル（esa config set / esa sync add で更新できる。手で編集してもよい）\n" +
	"# profile: 使用する Chrome プロファイル名（auto でログイン済みを自動検出）\n" +
	"# team: チーム名（サブドメイン）\n" +
	"# sync: esa sync の対象（name / category / dir）\n"

// parseConfigDoc は config.yml の内容を YAML のノードとして読む。最上位はマッピング 1 つに限る。
// 空・コメントだけの内容なら、そのコメントを持つ空のマッピングを返す（無ければ既定のヘッダ）。
func parseConfigDoc(data []byte) (*yaml.Node, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // BOM（残すとコメントだけのファイルのコメントを取りこぼす）
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// `---` で区切った 2 つ目以降の文書は読まれない。黙って無視すると、書いたはずの設定が無いことになり、
	// 書き戻しでも消える。末尾の `---` だけの空の文書は許す。
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || !isEmptyYAMLDoc(&extra) {
			return nil, errors.New("文書が複数あります（--- で区切らず、1 つにまとめてください）")
		}
	}
	if doc.Kind == 0 || isEmptyYAMLDoc(&doc) {
		// コメントだけのファイルなら、そのコメントを残す（既定のヘッダに置き換えない）。
		head := strings.TrimSuffix(configFileHeader, "\n")
		if c := strings.TrimSpace(string(data)); c != "" && strings.HasPrefix(c, "#") {
			head = c
		}
		return &yaml.Node{Kind: yaml.DocumentNode, HeadComment: head, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("最上位がマッピング（key: value の並び）ではありません")
	}
	// profile / team の読み込み（loadFileConfig）と同じ読み方で読めるかを確かめる。重複したキー・team: [a] のような形は
	// ノードとしては読めてしまうが、loadFileConfig は失敗する。ここで止めないと、sync add がそのまま書き、
	// 重複した sync: の 2 つ目以降が黙って読まれなくなる（issue 011 の red team）。
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, err
	}
	// 最上位は常にブロック形式で書く。空のマッピングは {} と書かれ、読み直すと flow 形式のまま以後の書き戻しが 1 行に詰まる。
	doc.Content[0].Style &^= yaml.FlowStyle
	// sync.yml の中身をそのまま貼った形。黙って読むと sync の対象が 0 件になる。
	if mappingValue(doc.Content[0], "targets") != nil {
		return nil, errors.New("最上位に targets: があります（v0.1.8 までの sync.yml の書き方です。targets: を sync: に書き換えてください）")
	}
	return &doc, nil
}

// readConfigDoc は config.yml を読む。無ければ既定のヘッダを持つ空の文書。
func readConfigDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	doc, err := parseConfigDoc(data)
	if err != nil {
		return nil, fmt.Errorf("%s を読めないため書き込みを中止しました（直してから再実行してください）: %w", path, err)
	}
	return doc, nil
}

// writeConfigDoc は文書を config.yml に書く。validate で書く内容を検査してから、同じディレクトリの一時ファイル経由で置き換える。
func writeConfigDoc(path string, doc *yaml.Node, validate func([]byte) error) error {
	// 最後のキーを消してマッピングが空になったら、末尾へ付け替えたコメントを文書の先頭へ回す（{} の後ろに回さない）。
	if top := doc.Content[0]; len(top.Content) == 0 && top.FootComment != "" {
		doc.HeadComment = joinComments(doc.HeadComment, top.FootComment)
		top.FootComment = ""
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2) // 既定の 4 だと、手で書く 2 桁の YAML と混ざる
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	out := buf.Bytes()
	if _, err := parseConfigDoc(out); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(out); err != nil {
			return err
		}
	}
	// config.yml がリンク（dotfiles の実体を指す等）なら実体へ書く。リンクごと rename で置き換えると、以後は別のファイルになる。
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%s のリンク先を解決できません: %w", path, err)
		}
		path = real
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	perm := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm() // 既存のファイルの権限は保つ（以前の os.WriteFile と同じ）
		// 🚨 読み取り専用のファイルは書かない。rename で置き換えるので、書けない権限でも上書きできてしまう
		// （以前の os.WriteFile は EACCES で止まっていた。issue 011 の red team）。
		if perm&0o200 == 0 {
			return fmt.Errorf("%s は書き込みできない権限（%v）です", path, perm)
		}
	}
	return writeFileAtomic(path, out, perm)
}

// mappingValue は最上位のマッピングからキーの値のノードを返す（無ければ nil）。
func mappingValue(top *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == key {
			return top.Content[i+1]
		}
	}
	return nil
}

// setMappingScalar はキーの値を文字列にする（無ければ末尾に足す）。value が空ならキーごと消す。
//
// 🚨 値が変わらないキーには触らない。触ると、アンカー（&a）を持つ値を置き換えてエイリアスを壊し、
// 空の profile: を消すときに、そのキーに付いたコメント（ファイル先頭のコメントは最初のキーに付く）まで消す（issue 011 の red team）。
// キーを消すときは、キーに付いたコメントを次のキーか、マッピングの末尾へ移す。
func setMappingScalar(top *yaml.Node, key, value string) {
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != key {
			continue
		}
		k, v := top.Content[i], top.Content[i+1]
		// マッピングやリストの profile / team は parseConfigDoc（loadFileConfig と同じ読み方の検査）が先に拒むので、ここに来るのは
		// スカラーかエイリアス（*a）。エイリアスは Value がアンカー名なので値の比較が一致せず書き換わり、同じ値のリテラルになる（受容。issue 011）
		cur := v.Value
		if v.Tag == "!!null" {
			cur = ""
		}
		if cur == value {
			return
		}
		if value == "" {
			carry := joinComments(k.HeadComment, v.HeadComment, k.FootComment, v.FootComment)
			top.Content = append(top.Content[:i], top.Content[i+2:]...)
			if carry != "" {
				if i < len(top.Content) {
					top.Content[i].HeadComment = joinComments(carry, top.Content[i].HeadComment)
				} else {
					top.FootComment = joinComments(carry, top.FootComment)
				}
			}
			return
		}
		*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, LineComment: v.LineComment, HeadComment: v.HeadComment, FootComment: v.FootComment}
		return
	}
	if value == "" {
		return
	}
	top.Content = append(top.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// joinComments は空でないコメントを改行でつなぐ。
func joinComments(cs ...string) string {
	var out []string
	for _, c := range cs {
		if c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, "\n")
}
