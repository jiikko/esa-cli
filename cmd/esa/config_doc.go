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
	return writeFileAtomic(path, out, 0o600)
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
// 既存のキーの前後のコメントは残す。
func setMappingScalar(top *yaml.Node, key, value string) {
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != key {
			continue
		}
		if value == "" {
			top.Content = append(top.Content[:i], top.Content[i+2:]...)
			return
		}
		v := top.Content[i+1]
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
