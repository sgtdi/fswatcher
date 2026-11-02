//go:build (darwin && !cgo) || freebsd

package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestKqueueUnhiddenPathsRestoreCoverage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		directory  bool
		addedLater bool
	}{
		{name: "initial-file"},
		{name: "initial-directory", directory: true},
		{name: "added-file", addedLater: true},
		{name: "added-directory", directory: true, addedLater: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			path := filepath.Join(root, "initially-hidden")
			initial := path
			if tc.addedLater {
				initial = filepath.Join(canonicalTempDir(t), "initially-hidden")
			}
			file := initial
			if tc.directory {
				require.NoError(t, os.Mkdir(initial, 0700))
				file = filepath.Join(initial, "existing.txt")
			}
			require.NoError(t, os.WriteFile(file, []byte("old"), 0600))
			const hiddenFlag = 0x00008000 // UF_HIDDEN on macOS and FreeBSD.
			if err := unix.Chflags(initial, hiddenFlag); err != nil {
				t.Skipf("UF_HIDDEN is unavailable: %v", err)
			}
			t.Cleanup(func() {
				_ = unix.Chflags(initial, 0)
				_ = unix.Chflags(path, 0)
			})
			ready := make(chan struct{})
			w, err := New(WithPath(root), WithReadyChannel(ready), WithCooldown(time.Millisecond), WithSeverity(SeverityNone))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- w.Watch(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-result:
					require.NoError(t, err)
				case <-time.After(5 * time.Second):
					t.Error("watcher did not stop")
				}
			})
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("watcher did not become ready")
			}
			waitForPath := func(path string) {
				t.Helper()
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				for {
					select {
					case event, open := <-w.Events():
						require.True(t, open, "watcher stopped unexpectedly")
						if event.Path == path {
							return
						}
					case <-timer.C:
						t.Fatalf("no event for unhidden path %s", path)
					}
				}
			}
			if tc.addedLater {
				require.NoError(t, os.Rename(initial, path))
				// A visible sibling event proves the parent scan has processed
				// both entries before we change the hidden attribute.
				marker := filepath.Join(root, "zz-registered.txt")
				require.NoError(t, os.WriteFile(marker, []byte("ready"), 0600))
				waitForPath(marker)
				file = path
				if tc.directory {
					file = filepath.Join(path, "existing.txt")
				}
			}
			require.NoError(t, unix.Chflags(path, 0))
			waitForPath(path)
			require.NoError(t, os.WriteFile(file, []byte("new"), 0600))
			waitForPath(file)
		})
	}
}
