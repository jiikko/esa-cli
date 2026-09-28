package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// syncLock は 1 つの書き出し先への --apply を 1 プロセスに限るロック。
//
// 🚨 これが無いと、同じ dir へ 2 本の --apply が同時に走ったとき、決まった名前の一時ファイルを互いに
// 消し合い、「書いた」と報告した側の本文が書き出し先に残らない（red team で 300 回中 11 回再現）。
// flock はプロセスが死ぬとカーネルが外すので、SIGKILL・電源断の後に古いロックで止まることは無い。
// ロックのファイルは書き出し先ではなく設定ディレクトリに置く（~/.claude/skills のような書き出し先に
// 記事以外のファイルを増やさないため）。別の XDG_CONFIG_HOME で動くプロセスどうしは排他にならない。
type syncLock struct{ f *os.File }

func acquireSyncLock(dir string) (*syncLock, error) {
	cfgDir, err := configDir()
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(cfgDir, "locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("--apply のロック用ディレクトリ %s を作れません（同じ dir への同時実行を防ぐために使います）: %w", lockDir, err)
	}
	sum := sha256.Sum256([]byte(syncPathKey(dir))) // APFS で同じディレクトリなら同じロック
	path := filepath.Join(lockDir, "sync-"+hex.EncodeToString(sum[:8])+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("--apply のロック %s を作れません（同じ dir への同時実行を防ぐために使います）: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("別の esa sync --apply が %s に書き込み中です。終わってから再実行してください", dir)
		}
		return nil, fmt.Errorf("ロック %s を取れません: %w", path, err)
	}
	return &syncLock{f: f}, nil
}

func (l *syncLock) release() {
	if l != nil && l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		_ = l.f.Close()
	}
}
