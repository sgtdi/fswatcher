//go:build (darwin && !cgo) || freebsd || openbsd || netbsd || dragonfly

package fswatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func createKqueueBenchmarkTree(tb testing.TB, root string, dirs, filesPerDir int) {
	tb.Helper()
	for i := 0; i < dirs; i++ {
		dir := filepath.Join(root, fmt.Sprintf("dir_%04d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", dir, err)
		}
		for j := 0; j < filesPerDir; j++ {
			file := filepath.Join(dir, fmt.Sprintf("file_%04d.txt", j))
			if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
				tb.Fatalf("write %s: %v", file, err)
			}
		}
	}
}

func BenchmarkKqueueAddWatch(b *testing.B) {
	const (
		dirs        = 100
		filesPerDir = 50
	)

	root := canonicalTempDir(b)
	createKqueueBenchmarkTree(b, root, dirs, filesPerDir)

	wIface, err := New()
	if err != nil {
		b.Fatalf("new watcher: %v", err)
	}
	w := wIface.(*watcher)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k, err := newKqueue(w)
		if err != nil {
			b.Fatalf("new kqueue: %v", err)
		}

		if err := k.addWatch(&WatchPath{Path: root, Depth: WatchNested}); err != nil {
			b.Fatalf("addWatch: %v", err)
		}

		k.mu.Lock()
		for fd := range k.wds {
			_ = unix.Close(fd)
		}
		_ = unix.Close(k.kqFd)
		_ = unix.Close(k.wakeRead)
		_ = unix.Close(k.wakeWrite)
		k.wds = nil
		k.paths = nil
		k.dirs = nil
		k.mu.Unlock()
	}
}
