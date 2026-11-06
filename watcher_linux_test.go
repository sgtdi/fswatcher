//go:build linux

package fswatcher

import (
	"context"
	"errors"
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
		t.Skip("requires CAP_SYS_ADMIN and CAP_DAC_READ_SEARCH")
	}
	root := canonicalTempDir(t)
	for _, dir := range []string{"a/nested", "b"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0700))
	}
	ready := make(chan struct{})
	wi, err := New(WithPath(root), WithLinuxPlatform(PlatformFanotify), WithReadyChannel(ready), WithCooldown(time.Millisecond), WithSeverity(SeverityDebug), WithLogFile("stdout"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	var watchErr error
	go func() { watchErr = wi.Watch(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()
	select {
	case <-ready:
	case <-stopped:
		if os.Getenv("FSWATCHER_REQUIRE_FANOTIFY") != "1" && (errors.Is(watchErr, unix.EPERM) || errors.Is(watchErr, unix.EOPNOTSUPP) || errors.Is(watchErr, unix.ENOSYS)) {
			t.Skipf("fanotify unavailable: %v", watchErr)
		}
		require.NoError(t, watchErr)
		t.Fatal("watcher stopped before ready")
	case <-time.After(5 * time.Second):
		t.Fatal("fanotify did not become ready")
	}
	for _, name := range []string{"a/nested/one.txt", "b/two.txt"} {
		file := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(file, []byte("data"), 0600))
		timeout := time.NewTimer(3 * time.Second)
	found:
		for {
			select {
			case event := <-wi.Events():
				if event.Path == file {
					break found
				}
			case <-timeout.C:
				t.Fatalf("no fanotify event for %s", file)
			}
		}
		timeout.Stop()
	}
	cancel()
	<-stopped
	require.NoError(t, watchErr)
}

func TestFanotifyRecursiveMarksAndDynamicDirectories(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing", "nested")
	require.NoError(t, os.MkdirAll(existing, 0o700))

	w, err := New(WithPath(root), WithSeverity(SeverityNone))
	require.NoError(t, err)
	wImpl := w.(*watcher)
	marked := make(map[string]int)
	markCalls := make(map[string]int)
	p := &fanotify{
		fd:         -1,
		paths:      make(map[string]struct{}),
		roots:      make(map[string]*fanotifyRoot),
		markRefs:   make(map[string]int),
		mountCache: make(map[uint64]string),
		markFn: func(path string, add bool) error {
			if add {
				marked[path]++
				markCalls["+"+path]++
			} else {
				marked[path]--
				markCalls["-"+path]++
				if marked[path] == 0 {
					delete(marked, path)
				}
			}
			return nil
		},
	}
	t.Cleanup(func() { wImpl.aggregator.close() })

	wp, err := validateWatchPath(root, WatchNested)
	require.NoError(t, err)
	require.NoError(t, p.addWatch(wImpl, wp))
	for _, path := range []string{root, filepath.Join(root, "existing"), existing} {
		assert.Equal(t, 1, marked[path], "existing directory should have a fanotify mark")
	}

	created := filepath.Join(root, "created")
	createdChild := filepath.Join(created, "child")
	require.NoError(t, os.MkdirAll(createdChild, 0o700))
	wImpl.dispatchFanotifyEvent(p, created, unix.FAN_CREATE|unix.FAN_ONDIR)
	assert.Equal(t, 1, marked[created], "new directory should be marked")
	assert.Equal(t, 1, marked[createdChild], "existing descendants should be marked")

	require.NoError(t, os.RemoveAll(created))
	wImpl.dispatchFanotifyEvent(p, created, unix.FAN_DELETE|unix.FAN_ONDIR)
	assert.NotContains(t, marked, created)
	assert.NotContains(t, marked, createdChild)
	assert.Equal(t, 1, markCalls["-"+created])
	assert.Equal(t, 1, markCalls["-"+createdChild])

	hidden := filepath.Join(root, ".hidden")
	require.NoError(t, os.Mkdir(hidden, 0o700))
	wImpl.dispatchFanotifyEvent(p, hidden, unix.FAN_CREATE|unix.FAN_ONDIR)
	assert.NotContains(t, marked, hidden, "hidden directories should stay unmarked by default")
}

func TestFanotifyMarkFailureRollsBackAndReportsCleanupErrors(t *testing.T) {
	markErr := errors.New("cannot add mark")
	rollbackA := errors.New("cannot remove mark a")
	rollbackB := errors.New("cannot remove mark b")
	for _, tc := range []struct {
		name     string
		failures map[string]error
	}{
		{name: "successful rollback"},
		{name: "rollback continues after cleanup failures", failures: map[string]error{"a": rollbackA, "b": rollbackB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			p := &fanotify{
				markRefs: map[string]int{"shared": 1},
				markFn: func(path string, add bool) error {
					if add {
						calls = append(calls, "+"+path)
						if path == "failed" {
							return markErr
						}
						return nil
					}
					calls = append(calls, "-"+path)
					return tc.failures[path]
				},
			}
			root := &fanotifyRoot{dirs: make(map[string]struct{})}
			err := p.addDirectoriesLocked(root, []string{"shared", "a", "b", "c", "failed"})
			require.ErrorIs(t, err, markErr)
			for path, cleanupErr := range tc.failures {
				require.ErrorIs(t, err, cleanupErr)
				require.ErrorContains(t, err, `rollback fanotify mark "`+path+`"`)
			}
			require.Equal(t, []string{"+a", "+b", "+c", "+failed", "-c", "-b", "-a"}, calls,
				"rollback must attempt every new mark and preserve the shared mark")
			wantRefs := map[string]int{"shared": 1}
			wantDirs := make(map[string]struct{})
			for path := range tc.failures {
				wantRefs[path] = 1
				wantDirs[path] = struct{}{}
			}
			require.Equal(t, wantRefs, p.markRefs)
			require.Equal(t, wantDirs, root.dirs)
		})
	}
}

func TestFanotifyOverflowEmitsEventAndReconcilesMarks(t *testing.T) {
	root := t.TempDir()
	w, err := New(WithPath(root), WithCooldown(time.Millisecond), WithSeverity(SeverityNone))
	require.NoError(t, err)
	wImpl := w.(*watcher)
	marked := make(map[string]bool)
	p := &fanotify{
		fd:         -1,
		paths:      make(map[string]struct{}),
		roots:      make(map[string]*fanotifyRoot),
		markRefs:   make(map[string]int),
		mountCache: make(map[uint64]string),
		markFn: func(path string, add bool) error {
			if add {
				marked[path] = true
			} else {
				delete(marked, path)
			}
			return nil
		},
	}
	t.Cleanup(func() { wImpl.aggregator.close() })

	wp, err := validateWatchPath(root, WatchNested)
	require.NoError(t, err)
	require.NoError(t, p.addWatch(wImpl, wp))
	missed := filepath.Join(root, "created-before-overflow")
	require.NoError(t, os.Mkdir(missed, 0o700))

	wImpl.dispatchFanotifyEvent(p, "", unix.FAN_Q_OVERFLOW)
	assert.True(t, marked[missed], "overflow reconciliation should restore marks for new directories")
	select {
	case event := <-w.Events():
		assert.Contains(t, event.Types, EventOverflow)
	case <-time.After(time.Second):
		t.Fatal("expected an overflow event")
	}
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
