//go:build darwin && !cgo

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

// TestKqueue_Backend tests the kqueue backend initialization
func TestKqueue_Backend(t *testing.T) {
	tempDir := t.TempDir()

	readyChan := make(chan struct{})
	w, err := New(
		WithPath(tempDir),
		WithReadyChannel(readyChan),
		WithCooldown(10*time.Millisecond),
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

	// Ensure the platform
	wImpl := w.(*watcher)
	wImpl.streamMu.Lock()
	platform := wImpl.streams["platform"]
	wImpl.streamMu.Unlock()
	assert.IsType(t, &kqueue{}, platform, "Should be using kqueue backend")

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

	testFile := filepath.Join(tempDir, "test_kqueue_backend.txt")
	err = os.WriteFile(testFile, []byte("data"), 0644)
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond)
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
	assert.True(t, found, "Should have received an event for the file using kqueue")
}

func TestNewKqueue(t *testing.T) {
	w, err := New()
	require.NoError(t, err)

	wImpl := w.(*watcher)
	k, err := newKqueue(wImpl)
	require.NoError(t, err)
	require.NotNil(t, k)
	assert.Greater(t, k.kqFd, 0)
	assert.NotNil(t, k.wds)
	assert.NotNil(t, k.paths)
	assert.NotNil(t, k.dirs)

	// Cleanup
	unix.Close(k.kqFd)
}

func TestStartPlatform(t *testing.T) {
	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done, err := wImpl.startPlatform(ctx)
	require.NoError(t, err)
	require.NotNil(t, done)

	// Verify streams are set
	wImpl.streamMu.Lock()
	platform := wImpl.streams["platform"]
	wImpl.streamMu.Unlock()
	assert.NotNil(t, platform)
	assert.IsType(t, &kqueue{}, platform)

	// Clean shutdown
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for platform shutdown")
	}
}

func TestRunKqueueLoop(t *testing.T) {
	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	k, err := newKqueue(wImpl)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	// Start loop in background
	go wImpl.runKqueueLoop(ctx, k, done)

	// It should be running. Cancel context to stop it.
	cancel()

	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for runKqueueLoop to exit")
	}

	// Verify cleanup
	k.mu.Lock()
	assert.Nil(t, k.wds)
	assert.Nil(t, k.paths)
	assert.Nil(t, k.dirs)
	k.mu.Unlock()
}

func TestProcessKqueueEvents(t *testing.T) {
	// Create a temp file
	tmpFile, err := os.CreateTemp("", "test_event_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	k, err := newKqueue(wImpl)
	require.NoError(t, err)
	defer unix.Close(k.kqFd)

	// Manually add watch to populate maps
	k.mu.Lock()
	err = k.addSingleWatchLocked(tmpFile.Name())
	k.mu.Unlock()
	require.NoError(t, err)

	k.mu.Lock()
	fd := k.paths[tmpFile.Name()]
	k.mu.Unlock()

	// Construct a fake event
	events := []unix.Kevent_t{
		{
			Ident:  uint64(fd),
			Fflags: unix.NOTE_WRITE,
		},
	}

	// processKqueueEvents uses w.handlePlatformEvent which sends to w.events or w.batcher
	// We need to consume w.events
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case evt := <-wImpl.events:
			assert.Equal(t, tmpFile.Name(), evt.Path)
			assert.Contains(t, evt.Types, EventMod)
		case <-time.After(time.Second):
			t.Error("timeout waiting for event")
		}
	}()

	wImpl.processKqueueEvents(k, events)
	wg.Wait()
}

func TestScanDirectoryLocked(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_scan_dir_*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	file1 := filepath.Join(tmpDir, "file1.txt")
	err = os.WriteFile(file1, []byte("content"), 0644)
	require.NoError(t, err)

	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	k, err := newKqueue(wImpl)
	require.NoError(t, err)
	defer unix.Close(k.kqFd)

	// Add the directory itself first (usually how it works)
	k.mu.Lock()
	err = k.addSingleWatchLocked(tmpDir)
	k.mu.Unlock()
	require.NoError(t, err)

	// Now scan
	k.mu.Lock()
	wImpl.scanDirectoryLocked(k, tmpDir)
	k.mu.Unlock()

	// Check if file1 is watched
	k.mu.Lock()
	_, exists := k.paths[file1]
	k.mu.Unlock()
	assert.True(t, exists, "file1 should be watched after scan")

	// Check if event was emitted
	select {
	case evt := <-wImpl.events:
		assert.Equal(t, file1, evt.Path)
		assert.Contains(t, evt.Types, EventCreate)
	case <-time.After(time.Second):
		t.Error("timeout waiting for scan event")
	}
}

func TestAddWatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_add_watch_*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	file1 := filepath.Join(tmpDir, "file1.txt")
	err = os.WriteFile(file1, []byte("content"), 0644)
	require.NoError(t, err)

	file2 := filepath.Join(subDir, "file2.txt")
	err = os.WriteFile(file2, []byte("content"), 0644)
	require.NoError(t, err)

	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	k, err := newKqueue(wImpl)
	require.NoError(t, err)
	defer unix.Close(k.kqFd)

	wp := &WatchPath{Path: tmpDir}
	err = k.addWatch(wp)
	require.NoError(t, err)

	k.mu.Lock()
	defer k.mu.Unlock()

	assert.Contains(t, k.paths, tmpDir)
	assert.Contains(t, k.paths, subDir)
	assert.Contains(t, k.paths, file1)
	assert.Contains(t, k.paths, file2)
	assert.True(t, k.dirs[tmpDir])
	assert.True(t, k.dirs[subDir])
}

func TestAddSingleWatchLocked(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_single_watch_*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	k, err := newKqueue(wImpl)
	require.NoError(t, err)
	defer unix.Close(k.kqFd)

	k.mu.Lock()
	err = k.addSingleWatchLocked(tmpFile.Name())
	k.mu.Unlock()
	require.NoError(t, err)

	k.mu.Lock()
	fd, exists := k.paths[tmpFile.Name()]
	path, ok := k.wds[fd]
	k.mu.Unlock()

	assert.True(t, exists)
	assert.True(t, ok)
	assert.Equal(t, tmpFile.Name(), path)
	assert.Greater(t, fd, 0)
}

func TestRemoveWatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_remove_watch_*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	file1 := filepath.Join(tmpDir, "file1.txt")
	err = os.WriteFile(file1, []byte("content"), 0644)
	require.NoError(t, err)

	w, err := New()
	require.NoError(t, err)
	wImpl := w.(*watcher)

	k, err := newKqueue(wImpl)
	require.NoError(t, err)
	defer unix.Close(k.kqFd)

	wp := &WatchPath{Path: tmpDir}
	err = k.addWatch(wp)
	require.NoError(t, err)

	// Remove everything
	err = k.removeWatch(tmpDir)
	require.NoError(t, err)

	k.mu.Lock()
	defer k.mu.Unlock()

	// Should be empty as we removed the root
	assert.Empty(t, k.paths)
	assert.Empty(t, k.wds)
}
