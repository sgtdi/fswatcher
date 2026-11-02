package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSymlinkRootIsRecursiveAndDeletedAliasCanBeDropped(t *testing.T) {
	target, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	nested := filepath.Join(target, "nested")
	require.NoError(t, os.Mkdir(nested, 0700))
	file := filepath.Join(nested, "existing.dat")
	require.NoError(t, os.WriteFile(file, []byte("old"), 0600))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ready := make(chan struct{})
	w, err := New(WithPath(alias), WithReadyChannel(ready), WithCooldown(time.Millisecond), WithSeverity(SeverityNone))
	require.NoError(t, err)
	require.Equal(t, []string{target}, w.Paths())
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- w.Watch(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped; require.NoError(t, <-result) }()
	select {
	case <-ready:
	case <-stopped:
		t.Fatal("startup failed")
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	require.NoError(t, os.WriteFile(file, []byte("new"), 0600))
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event := <-w.Events():
			if event.Path == file {
				require.NoError(t, os.Remove(alias))
				require.NoError(t, w.DropPath(alias))
				require.Empty(t, w.Paths())
				return
			}
		case <-timeout.C:
			t.Fatal("symlink root did not watch preexisting nested files")
		}
	}
}

func TestDisappearedRootFailsStartup(t *testing.T) {
	root := t.TempDir()
	ready := make(chan struct{})
	w, err := New(WithPath(root), WithReadyChannel(ready), WithSeverity(SeverityNone))
	require.NoError(t, err)
	require.NoError(t, os.Remove(root))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.Error(t, w.Watch(ctx))
	select {
	case <-ready:
		t.Fatal("failed watcher reported ready")
	default:
	}
	_, open := <-w.Events()
	require.False(t, open)
}

func TestTopLevelDeliversRootRemoval(t *testing.T) {
	w, err := New(WithPath(t.TempDir(), WithDepth(WatchTopLevel)), WithSeverity(SeverityNone))
	require.NoError(t, err)
	impl := w.(*watcher)
	root := w.Paths()[0]
	impl.handlePlatformEvent(WatchEvent{Path: root, Types: []EventType{EventRemove}})
	impl.aggregator.close()
	require.Len(t, impl.events, 1)
	require.Contains(t, (<-impl.events).Types, EventRemove)
}
