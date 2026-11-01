//go:build windows

package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatcher_Windows(t *testing.T) {
	tempDir := canonicalTempDir(t)

	w, err := New(WithPath(tempDir))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.Watch(ctx)
	}()

	time.Sleep(100 * time.Millisecond) // Give the watcher time to start

	var event WatchEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		evt := <-w.Events()
		mu.Lock()
		event = evt
		mu.Unlock()
		close(done)
	}()

	testFile := filepath.Join(tempDir, "test.txt")
	f, err := os.Create(testFile)
	require.NoError(t, err)
	f.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, testFile, event.Path)
	assert.Contains(t, event.Types, EventCreate)
}

func TestWindowsRootRemovalKeepsOtherRootsRunning(t *testing.T) {
	root, remaining := canonicalTempDir(t), canonicalTempDir(t)
	ready := make(chan struct{})
	w, err := New(WithPath(root), WithPath(remaining), WithReadyChannel(ready), WithCooldown(time.Millisecond), WithSeverity(SeverityNone))
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
	waitForEvent := func(path string, kind EventType) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event, open := <-w.Events():
				require.True(t, open, "watcher stopped after a root was deleted")
				if event.Path == path {
					for _, actual := range event.Types {
						if actual == kind {
							return
						}
					}
				}
			case <-timer.C:
				t.Fatalf("no %v event for %s", kind, path)
			}
		}
	}
	require.NoError(t, os.Remove(root))
	// Wait for native deletion handling before DropPath, so this cannot pass
	// merely because DropPath canceled the pending read first.
	waitForEvent(root, EventRemove)
	require.True(t, w.IsRunning())
	require.NoError(t, w.DropPath(root))
	require.Equal(t, []string{remaining}, w.Paths())
	file := filepath.Join(remaining, "still-watched.txt")
	require.NoError(t, os.WriteFile(file, []byte("data"), 0600))
	waitForEvent(file, EventCreate)
}
