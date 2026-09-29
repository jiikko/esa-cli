package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// syncLock は 1 つの書き出し先への --apply を 1 プロセスに限るロック。
//
// 🚨 これが無いと、同じ dir へ 2 本の --apply が同時に走ったとき、決まった名前の一時ファイルを互いに
// 消し合い、「書いた」と報告した側の本文が書き出し先に残らない（red team で 300 回中 11 回再現）。
// flock はプロセスが死ぬとカーネルが外すので、SIGKILL・電源断の後に古いロックで止まることは無い。
// ロックのファイルは書き出し先ではなく設定ディレクトリに置く（~/.claude/skills のような書き出し先に
// 記事以外のファイルを増やさないため）。別の XDG_CONFIG_HOME で動くプロセスどうしは排他にならない。
//
// 🚨 鍵は 2 つ取る: パスの文字列（旧来の鍵）と、シンボリックリンクを解決した実パス（lockKeyPath）。
//   - 実パスの鍵が無いと、同じ実体を実パスとリンクのパス（dotfiles へのリンクにした ~/.claude/skills 等）の
//     2 つで登録したとき別のロックになり、2 本の --apply が同じ dir に同時に書けた（brew の v0.1.7 で実測）
//   - 文字列の鍵を外すと、ロックを取った後にリンクが指し直されたとき、同じ文字列で登録した 2 本が別の実パスの
//     鍵を取れてしまい、しかも両方が指し直した先へ書く（敵対的レビューが再現。旧実装では必ず排他だった）
//
// firmlink（/System/Volumes/Data/Users/... と /Users/...）は EvalSymlinks で解決されず別の鍵になる。
// そう書いて登録することは普通無いので扱わない。
type syncLock struct{ fs []*os.File }

func acquireSyncLock(dir string) (*syncLock, error) {
	cfgDir, err := configDir()
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(cfgDir, "locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("--apply のロック用ディレクトリ %s を作れません（同じ dir への同時実行を防ぐために使います）: %w", lockDir, err)
	}
	keys := []string{syncPathKey(dir)}
	if k := syncPathKey(lockKeyPath(dir)); k != keys[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys) // どの 2 本も同じ順で取る
	l := &syncLock{}
	for _, k := range keys {
		sum := sha256.Sum256([]byte(k)) // APFS で同じディレクトリなら同じロック
		path := filepath.Join(lockDir, "sync-"+hex.EncodeToString(sum[:8])+".lock")
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			l.release()
			return nil, fmt.Errorf("--apply のロック %s を作れません（同じ dir への同時実行を防ぐために使います）: %w", path, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			l.release()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("別の esa sync --apply が %s に書き込み中です。終わってから再実行してください", dir)
			}
			return nil, fmt.Errorf("ロック %s を取れません: %w", path, err)
		}
		l.fs = append(l.fs, f)
	}
	return l, nil
}

func (l *syncLock) release() {
	if l == nil {
		return
	}
	for _, f := range l.fs {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	l.fs = nil
}

// lockKeyPath は dir のシンボリックリンクを解決した絶対パスを返す（ロックの鍵にする）。
// dir がまだ無い（初回の --apply で作る）ときは、存在する一番深い親までを解決して残りをつなぐ。
// リンク先がまだ無いリンク（~/.claude/skills → ~/dotfiles/skills を張ったが実体はまだ無い）も、
// Readlink でたどって実体側のパスにそろえる（EvalSymlinks は失敗するので、字句のままだと別の鍵になる）。
// 解決できないとき（権限・リンクの循環）は解決できた所までで止める。
func lockKeyPath(dir string) string {
	p, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	var rest []string
	for hops := 0; ; {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 && hops < 40 {
			if target, err := os.Readlink(p); err == nil {
				hops++
				if !filepath.IsAbs(target) {
					// 🚨 親を解決してからつなぐ。親がリンクのとき、相対ターゲットの ".." をリンクの字句の上で畳むと
					// カーネルの解決（実体の親から上がる）と食い違い、別の鍵になる（敵対的レビュー 2 周目が再現）。
					target = filepath.Join(lockKeyPath(filepath.Dir(p)), target)
				}
				p = filepath.Clean(target)
				continue
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(append([]string{p}, rest...)...)
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}
