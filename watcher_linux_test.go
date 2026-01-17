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
)

// TestFanotify_Backend tests the fanotify backend initialization
func TestFanotify_Backend(t *testing.T) {
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
