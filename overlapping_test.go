package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOverlappingRootsRetainCoverage(t *testing.T) {
	for _, dropParent := range []bool{true, false} {
		name := "drop-child"
		if dropParent {
			name = "drop-parent"
		}
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			child := filepath.Join(root, "child")
			require.NoError(t, os.Mkdir(child, 0700))
			ready := make(chan struct{})
			options := []WatcherOpt{WithPath(root), WithReadyChannel(ready), WithCooldown(time.Millisecond), WithSeverity(SeverityNone)}
			if dropParent {
				options = append(options, WithPath(child))
			}
			w, err := New(options...)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			stopped := make(chan struct{})
			go func() { done <- w.Watch(ctx); close(stopped) }()
			defer func() { cancel(); <-stopped; require.NoError(t, <-done) }()
			select {
			case <-ready:
			case <-stopped:
				t.Fatal("startup failed")
			case <-time.After(5 * time.Second):
				t.Fatal("not ready")
			}
			if !dropParent {
				require.NoError(t, w.AddPath(child))
			}
			drop := child
			if dropParent {
				drop = root
			}
			require.NoError(t, w.DropPath(drop))
			file := filepath.Join(child, "still-watched.txt")
			require.NoError(t, os.WriteFile(file, []byte("data"), 0600))
			timeout := time.NewTimer(3 * time.Second)
			defer timeout.Stop()
			for {
				select {
				case event := <-w.Events():
					if event.Path == file {
						return
					}
				case <-timeout.C:
					t.Fatal("remaining root lost native coverage")
				}
			}
		})
	}
}
