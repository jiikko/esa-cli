//go:build darwin

package main

import (
	"bytes"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY は pty を開き、端末側（slave）を返す。配布先と CI が macOS なので darwin の ioctl で開く。
func openPTY(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("pty を開けない: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	fd := int(master.Fd())
	for _, req := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(fd, req, 0); err != nil {
			t.Fatalf("pty の ioctl %#x が失敗: %v", req, err)
		}
	}
	var name [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		t.Fatalf("pty の名前を取れない: %v", errno)
	}
	slave, err := os.OpenFile(string(name[:bytes.IndexByte(name[:], 0)]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("pty の端末側を開けない: %v", err)
	}
	t.Cleanup(func() { slave.Close() })
	return slave
}

// 端末なら色を付け、NO_COLOR / TERM=dumb なら付けないこと（色が二度と出なくなる退行を捕まえる）。
func TestSyncPaletteForTerminal(t *testing.T) {
	tty := openPTY(t)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if !syncPaletteFor(tty).on {
		t.Error("端末なのに色を付けない")
	}
	t.Setenv("NO_COLOR", "1")
	if syncPaletteFor(tty).on {
		t.Error("NO_COLOR なのに色を付けた")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if syncPaletteFor(tty).on {
		t.Error("TERM=dumb なのに色を付けた")
	}
}

// esa sync が書く先（stdout）と色を判定する先が一致すること。stdout が端末なら色あり、パイプなら色なし。
// stderr を逆の状態にして、判定する先を取り違えたら落ちるようにする。
func TestSyncStdoutPaletteFollowsStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	tty := openPTY(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	origOut, origErr := os.Stdout, os.Stderr
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })

	os.Stdout, os.Stderr = tty, w
	if out, pal := syncStdout(); out != tty || !pal.on {
		t.Errorf("stdout が端末なのに 書く先=%v 色=%v", out == tty, pal.on)
	}
	os.Stdout, os.Stderr = w, tty
	if out, pal := syncStdout(); out != w || pal.on {
		t.Errorf("stdout がパイプなのに 書く先=%v 色=%v", out == w, pal.on)
	}
}
