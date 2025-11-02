//go:build darwin

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

func TestWatcher_Darwin(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "TestWatcher_Darwin*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	tempDir, err = filepath.EvalSymlinks(tempDir)
	require.NoError(t, err)

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
	assert.Contains(t, []string{testFile, tempDir}, event.Path)
	assert.Contains(t, event.Types, EventCreate)
}
