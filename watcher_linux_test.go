//go:build linux

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
	"golang.org/x/sys/unix"
)

// TestFanotify_Backend tests the fanotify backend initialization
func TestFanotify_Backend(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Skipping fanotify test: requires root (CAP_SYS_ADMIN)")
	}
	// Fanotify requires CAP_SYS_ADMIN
	tempDir, err := os.MkdirTemp(".", "fanotify_test_")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)
	readyChan := make(chan struct{})
	w, err := New(
		WithPath(tempDir),
		WithLinuxPlatform(PlatformFanotify),
		WithReadyChannel(readyChan),
		WithCooldown(10*time.Millisecond),
		WithSeverity(SeverityDebug),
		WithLogFile("stdout"),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = w.Watch(ctx)
	}()

	select {
	case <-readyChan:
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher failed to start")
	}

	wImpl := w.(*watcher)
	wImpl.streamMu.Lock()
	platform := wImpl.streams["platform"]
	wImpl.streamMu.Unlock()
	assert.IsType(t, &fanotify{}, platform, "Should be using fanotify backend")

	var events []WatchEvent
	var mu sync.Mutex
	done := make(chan struct{})

	go func() {
		for event := range w.Events() {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
		close(done)
	}()

	testFile := filepath.Join(tempDir, "test_fanotify.txt")
	err = os.WriteFile(testFile, []byte("data"), 0644)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()

	found := false
	for _, e := range events {
		if e.Path == testFile {
			found = true
			break
		}
	}
	assert.True(t, found, "Should have received an event for the file using fanotify")
}

// TestInotify_Backend tests the inotify backend initialization
func TestInotify_Backend(t *testing.T) {
	tempDir := t.TempDir()

	readyChan := make(chan struct{})
	w, err := New(
		WithPath(tempDir),
		WithLinuxPlatform(PlatformInotify),
		WithReadyChannel(readyChan),
		WithCooldown(10*time.Millisecond),
		WithSeverity(SeverityDebug),
		WithLogFile("stdout"),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = w.Watch(ctx)
	}()

	select {
	case <-readyChan:
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher failed to start")
	}

	wImpl := w.(*watcher)
	wImpl.streamMu.Lock()
	platform := wImpl.streams["platform"]
	wImpl.streamMu.Unlock()
	assert.IsType(t, &inotify{}, platform, "Should be using inotify backend")

	var events []WatchEvent
	var mu sync.Mutex
	done := make(chan struct{})

	go func() {
		for event := range w.Events() {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
		close(done)
	}()

	testFile := filepath.Join(tempDir, "test_inotify.txt")
	err = os.WriteFile(testFile, []byte("data"), 0644)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()

	found := false
	for _, e := range events {
		if e.Path == testFile {
			found = true
			break
		}
	}
	assert.True(t, found, "Should have received an event for the file using inotify")
}

func TestInotify_RemoveWatchCleansState(t *testing.T) {
	dir := t.TempDir()

	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	p, err := newInotify()
	require.NoError(t, err)
	defer unix.Close(p.fd)

	wp, err := validateWatchPath(dir, WatchNested)
	require.NoError(t, err)

	require.NoError(t, p.addWatch(wImpl, wp))
	require.NoError(t, p.removeWatch(wp.Path))

	// Re-adding the same path must succeed after removal
	require.NoError(t, p.addWatch(wImpl, wp))
}

func TestInotifyAddWatchSkipsHiddenDirectoriesByDefault(t *testing.T) {
	root := t.TempDir()
	hiddenNested := filepath.Join(root, ".hidden", "nested")
	visibleNested := filepath.Join(root, "visible", "nested")
	require.NoError(t, os.MkdirAll(hiddenNested, 0o700))
	require.NoError(t, os.MkdirAll(visibleNested, 0o700))

	for _, tc := range []struct {
		name          string
		includeHidden bool
		wantHidden    bool
	}{
		{name: "hidden excluded by default"},
		{name: "WithHidden includes hidden", includeHidden: true, wantHidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := []WatcherOpt{}
			if tc.includeHidden {
				options = append(options, WithHidden())
			}
			w, err := New(options...)
			require.NoError(t, err)

			p, err := newInotify()
			require.NoError(t, err)
			t.Cleanup(func() { _ = unix.Close(p.fd) })

			wp, err := validateWatchPath(root, WatchNested)
			require.NoError(t, err)
			require.NoError(t, p.addWatch(w.(*watcher), wp))

			isWatched := func(path string) bool {
				p.mu.RLock()
				defer p.mu.RUnlock()
				for _, watched := range p.wds {
					if watched.Path == path {
						return true
					}
				}
				return false
			}

			assert.True(t, isWatched(visibleNested), "visible nested directory should be watched")
			assert.Equal(t, tc.wantHidden, isWatched(hiddenNested), "hidden nested directory registration")
		})
	}
}

func TestInotifyHiddenWatchRootCanBeDropped(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".hidden-root")
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o700))

	w, err := New(WithPath(root))
	require.NoError(t, err)
	wImpl := w.(*watcher)
	p, err := newInotify()
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(p.fd) })
	wImpl.streams = map[string]any{"platform": p}

	wp, err := validateWatchPath(root, WatchNested)
	require.NoError(t, err)
	require.NoError(t, p.addWatch(wImpl, wp))

	p.mu.RLock()
	rootWatched := false
	nestedWatched := false
	for _, watched := range p.wds {
		rootWatched = rootWatched || watched.Path == root
		nestedWatched = nestedWatched || watched.Path == nested
	}
	p.mu.RUnlock()
	assert.True(t, rootWatched, "the explicit hidden root remains removable")
	assert.False(t, nestedWatched, "hidden roots are not recursively scanned")

	require.NoError(t, wImpl.removeWatch(root))
	p.mu.RLock()
	defer p.mu.RUnlock()
	assert.Empty(t, p.wds, "dropping the hidden root removes its watch")
}
