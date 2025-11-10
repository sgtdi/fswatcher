package fswatcher

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var tempDir string

// mockWriter is a thread-safe io.Writer for capturing log messages
type mockWriter struct {
	mu       sync.Mutex
	messages []string
}

func (mw *mockWriter) Write(p []byte) (n int, err error) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	mw.messages = append(mw.messages, string(p))
	return len(p), nil
}

func (mw *mockWriter) getMessages() []string {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	// Return a copy
	msgs := make([]string, len(mw.messages))
	copy(msgs, mw.messages)
	return msgs
}

// TestMain init random temp dir
func TestMain(m *testing.M) {
	var err error

	tempDir, err = os.MkdirTemp("", fmt.Sprintf("temp-%d", rand.Intn(1000000)))
	if err != nil {
		fmt.Printf("Failed to create: %v\n", err)
		os.Exit(1)
	}

	tempDir, err = filepath.EvalSymlinks(tempDir)
	if err != nil {
		fmt.Printf("Failed to resolve symlinks: %v\n", err)
		os.Exit(1)
	}

	exitCode := m.Run()

	_ = os.RemoveAll(tempDir)
	os.Exit(exitCode)
}

func TestWatcher(t *testing.T) {

	t.Run("Watch", func(t *testing.T) {
		readyChan := make(chan struct{})

		w, err := New(
			WithPath(tempDir),
			WithCooldown(5*time.Millisecond),
			WithReadyChannel(readyChan),
			WithLogLevel(LogLevelNone),
		)
		require.NoError(t, err)
		require.NotNil(t, w)

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			err = w.Watch(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("Watcher exited with unexpected error: %v", err)
			}
		}()

		select {
		case <-readyChan:
		case <-time.After(5 * time.Second):
			t.Fatal("Watcher is not ready")
		}
		assert.True(t, w.IsRunning(), "Watcher should be running")

		var collectedEvents []WatchEvent
		var eventsMu sync.Mutex
		var eventsWg sync.WaitGroup
		eventsWg.Add(1)

		go func() {
			defer eventsWg.Done()
			for event := range w.Events() {
				eventsMu.Lock()
				collectedEvents = append(collectedEvents, event)
				eventsMu.Unlock()
			}
		}()

		expectedPaths := performOperations(t, tempDir, 1000)

		time.Sleep(6 * time.Second)

		cancel()
		wg.Wait()
		eventsWg.Wait()
		assert.False(t, w.IsRunning(), "Watcher should not be running after close")

		eventsMu.Lock()
		defer eventsMu.Unlock()

		receivedEvents := make(map[string]bool)
		for _, event := range collectedEvents {
			if event.Path == tempDir {
				continue
			}
			receivedEvents[event.Path] = true
		}

		type missingEventInfo struct {
			path   string
			action string
		}
		var missingEvents []missingEventInfo
		for path, action := range expectedPaths {
			if !receivedEvents[path] {
				missingEvents = append(missingEvents, missingEventInfo{path: path, action: action})
			}
		}

		var unexpectedEvents []string
		for path := range receivedEvents {
			if _, exists := expectedPaths[path]; !exists {
				relPath, _ := filepath.Rel(tempDir, path)
				unexpectedEvents = append(unexpectedEvents, relPath)
			}
		}

		assert.Empty(t, unexpectedEvents, "Should not have received any unexpected events")
		assert.Equal(t, len(expectedPaths), len(receivedEvents), "Should have received all expected events")

		if t.Failed() {
			t.Logf("Total expected events: %d", len(expectedPaths))
			t.Logf("Total received events: %d", len(receivedEvents))
			t.Logf("Total unexpected events: %d", len(unexpectedEvents))

			if len(missingEvents) > 0 {
				sort.Slice(missingEvents, func(i, j int) bool {
					return missingEvents[i].path < missingEvents[j].path
				})
				t.Logf("Missing events (%d):", len(missingEvents))
				for _, info := range missingEvents {
					relPath, _ := filepath.Rel(tempDir, info.path)
					t.Logf("  - ACTION: %-8s %s", info.action, relPath)
				}
			}
		}
	})

	t.Run("AddPath", func(t *testing.T) {
		// Additional temp dir
		tempT, _ := os.MkdirTemp("", "temp-*")
		dirT, _ := filepath.EvalSymlinks(tempT)
		defer func() { _ = os.RemoveAll(dirT) }()

		w, err := New(WithPath(tempDir))
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = w.Watch(ctx) }()
		time.Sleep(100 * time.Millisecond)
		require.True(t, w.IsRunning())

		var collectedEvents []WatchEvent
		var eventsMu sync.Mutex
		eventCollectionDone := make(chan struct{})
		go func() {
			for event := range w.Events() {
				eventsMu.Lock()
				collectedEvents = append(collectedEvents, event)
				eventsMu.Unlock()
			}
			close(eventCollectionDone)
		}()

		// Add a second dir
		err = w.AddPath(dirT)
		require.NoError(t, err)

		// Create a file in both
		file1 := filepath.Join(tempDir, "file1.txt")
		require.NoError(t, os.WriteFile(file1, []byte("data1"), 0644))
		file2 := filepath.Join(dirT, "file2.txt")
		require.NoError(t, os.WriteFile(file2, []byte("data2"), 0644))

		time.Sleep(500 * time.Millisecond)

		// Stop
		cancel()
		<-eventCollectionDone

		eventsMu.Lock()
		defer eventsMu.Unlock()

		eventPaths := make(map[string]bool)
		for _, e := range collectedEvents {
			eventPaths[e.Path] = true
		}

		assert.True(t, eventPaths[file1], "Should have seen event from initial path", tempDir)
		assert.True(t, eventPaths[file2], "Should have seen event from path added at runtime", dirT)
	})

	t.Run("DropPath", func(t *testing.T) {
		firstDir, err := os.MkdirTemp("", "temp-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(firstDir) }()
		firstDir, err = filepath.EvalSymlinks(firstDir)
		require.NoError(t, err)

		secondDir, err := os.MkdirTemp("", "temp-*")
		require.NoError(t, err)
		defer func() { _ = os.RemoveAll(secondDir) }()
		secondDir, err = filepath.EvalSymlinks(secondDir)
		require.NoError(t, err)

		// Watcher starts with first temp dir
		w, err := New(WithPath(firstDir), WithPath(secondDir))
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = w.Watch(ctx) }()
		time.Sleep(100 * time.Millisecond)
		require.True(t, w.IsRunning())

		var collectedEvents []WatchEvent
		var eventsMu sync.Mutex
		eventCollectionDone := make(chan struct{})
		go func() {
			for event := range w.Events() {
				eventsMu.Lock()
				collectedEvents = append(collectedEvents, event)
				eventsMu.Unlock()
			}
			close(eventCollectionDone)
		}()

		// Create a file in the firsDir
		firstFile := filepath.Join(firstDir, "firstFile.txt")
		require.NoError(t, os.WriteFile(firstFile, []byte("data2"), 0644))
		time.Sleep(200 * time.Millisecond) // Let event process

		// Drop firstDir
		err = w.DropPath(firstDir)
		require.NoError(t, err)

		// Create a file in the dropped firstDir
		secondFile := filepath.Join(firstDir, "secondFile.txt")
		require.NoError(t, os.WriteFile(secondFile, []byte("data1"), 0644))

		// Cread a file secondDir
		thirdFile := filepath.Join(secondDir, "thirdFile.txt")
		require.NoError(t, os.WriteFile(thirdFile, []byte("data4"), 0644))

		time.Sleep(500 * time.Millisecond)

		cancel()
		<-eventCollectionDone

		eventsMu.Lock()
		defer eventsMu.Unlock()

		eventPaths := make(map[string]bool)
		for _, e := range collectedEvents {
			eventPaths[e.Path] = true
		}

		assert.True(t, eventPaths[firstFile], "Should have seen initial event %v", eventPaths[firstFile])
		assert.False(t, eventPaths[secondFile], "Should NOT have seen event for file in removed path")
		assert.True(t, eventPaths[thirdFile], "Should have seen event for file in remaining path")
	})

	t.Run("Events", func(t *testing.T) {
		testChan := make(chan WatchEvent, 1)

		w := &watcher{
			events: testChan,
		}

		eventsChan := w.Events()
		require.NotNil(t, eventsChan, "Events() should not return a nil channel")

		// Compare pointer addresses
		assert.Equal(t,
			fmt.Sprintf("%p", testChan),
			fmt.Sprintf("%p", eventsChan),
			"Events() should return the same underlying channel instance",
		)

		expectedEvent := WatchEvent{Path: "/path/to/file.txt"}
		testChan <- expectedEvent

		select {
		case receivedEvent := <-eventsChan:
			assert.Equal(t, expectedEvent, receivedEvent, "Should receive the correct event from the channel")
		case <-time.After(1000 * time.Millisecond):
			t.Fatal("Timed out waiting to receive event from the channel")
		}
	})

	t.Run("Dropped", func(t *testing.T) {
		eventsChan := make(chan WatchEvent, 1)
		droppedChan := make(chan WatchEvent, 1)

		w := &watcher{
			events:  eventsChan,
			dropped: droppedChan,
			stats:   watcherStats{},
		}

		event1 := WatchEvent{Path: "/path/one"}
		event2 := WatchEvent{Path: "/path/two"}
		event3 := WatchEvent{Path: "/path/three"}

		w.sendToChannel(event1)

		receivedEvent1 := <-eventsChan
		assert.Equal(t, event1.Path, receivedEvent1.Path)
		assert.Equal(t, int64(1), atomic.LoadInt64(&w.stats.eventsProcessed), "Processed count should be 1")
		assert.Equal(t, int64(0), atomic.LoadInt64(&w.stats.eventsDropped), "Dropped count should be 0")

		// Fill the main channel buffer
		eventsChan <- WatchEvent{Path: "/path/filler"}

		// Send an event with a full main channel
		w.sendToChannel(event2)

		droppedEvent := <-droppedChan
		assert.Equal(t, event2.Path, droppedEvent.Path)
		assert.Equal(t, int64(1), atomic.LoadInt64(&w.stats.eventsProcessed), "Processed count should still be 1")
		assert.Equal(t, int64(1), atomic.LoadInt64(&w.stats.eventsDropped), "Dropped count should now be 1")

		// Verify the main channel was not affected.
		require.Len(t, eventsChan, 1, "Main channel should still contain the filler event")

		// Fill the dropped channel buffer
		droppedChan <- WatchEvent{Path: "/path/dropped_filler"}

		// Send an event with both full channels
		w.sendToChannel(event3)

		assert.Equal(t, int64(1), atomic.LoadInt64(&w.stats.eventsLost), "Lost count should now be 1")
		assert.Equal(t, int64(1), atomic.LoadInt64(&w.stats.eventsDropped), "Dropped count should still be 1")

		require.Len(t, eventsChan, 1)
		require.Len(t, droppedChan, 1)
	})

	t.Run("IsRunning", func(t *testing.T) {
		readyChan := make(chan struct{})
		w, err := New(WithPath(t.TempDir()), WithReadyChannel(readyChan))
		require.NoError(t, err)

		assert.False(t, w.IsRunning(), "Watcher should not be running before Watch is called")

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			err = w.Watch(ctx)
			assert.Nil(t, err, "Watcher should not have exited with an error")
		}()

		select {
		case <-readyChan:
		case <-time.After(2 * time.Second):
			t.Fatal("Watcher is not ready after 2 seconds")
		}

		assert.True(t, w.IsRunning(), "Watcher should be running")

		cancel()
		wg.Wait()

		assert.False(t, w.IsRunning(), "Watcher should not be running after Watch has exited")
	})

	t.Run("Stats", func(t *testing.T) {
		w, err := New()
		require.NoError(t, err)

		stats := w.Stats()
		assert.Equal(t, int64(0), stats.EventsProcessed)
		assert.WithinDuration(t, time.Now(), stats.StartTime, 1*time.Second)

		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = w.Watch(ctx) }()
		time.Sleep(50 * time.Millisecond)

		stats = w.Stats()
		assert.Greater(t, stats.Uptime.Nanoseconds(), int64(0))

		cancel()
	})

	t.Run("Close", func(t *testing.T) {
		w := &watcher{
			done:           make(chan struct{}),
			isShuttingDown: atomic.Bool{},
		}

		doneChanClosed := make(chan bool, 1)
		go func() {
			<-w.done
			doneChanClosed <- true
		}()

		w.Close()

		assert.True(t, w.isShuttingDown.Load(), "isShuttingDown flag should be true after the first call")

		select {
		case wasClosed := <-doneChanClosed:
			assert.True(t, wasClosed, "The done channel should be closed on the first call")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timed out waiting for the done channel to be closed")
		}
	})

}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	if n <= 0 {
		return ""
	}
	ret := make([]byte, n)
	for i := range n {
		ret[i] = letters[rand.Intn(len(letters))]
	}
	return string(ret)
}

func randInt(min, max int) int {
	if min > max {
		min, max = max, min
	}
	return rand.Intn(max-min+1) + min
}

// performOperations performs a number of random CRUD ops
func performOperations(t *testing.T, basePath string, numOps int) map[string]string {

	dirs := []string{basePath}
	var files []string
	extensions := []string{".txt", ".log", ".dat", ".json", ".xml"}

	expectedPaths := make(map[string]string)

	logAction := func(action, path string) {
		relPath, _ := filepath.Rel(basePath, path)
		t.Logf("-> ACTION: %-8s %s", action, relPath)
	}

	buildOps := numOps / 2
	for range buildOps {
		op := randInt(0, 1)
		switch op {
		case 0: // Create File
			if len(dirs) == 0 {
				continue
			}
			parentDir := dirs[randInt(0, len(dirs)-1)]
			filePath := filepath.Join(parentDir, randString(8)+extensions[randInt(0, len(extensions)-1)])
			require.NoError(t, os.WriteFile(filePath, []byte(randString(10)), 0644))
			files = append(files, filePath)
			logAction("CREATE F", filePath)
			expectedPaths[filePath] = "CREATE F"
		case 1: // Create Directory
			if len(dirs) == 0 {
				continue
			}
			parentDir := dirs[randInt(0, len(dirs)-1)]
			dirPath := filepath.Join(parentDir, "dir_"+randString(6))
			require.NoError(t, os.Mkdir(dirPath, 0755))
			dirs = append(dirs, dirPath)
			logAction("CREATE D", dirPath)
			expectedPaths[dirPath] = "CREATE D"
		}
	}

	time.Sleep(100 * time.Millisecond)

	actionOps := numOps - buildOps
	for range actionOps {
		op := randInt(0, 3)
		switch op {
		case 0: // Modify File
			if len(files) == 0 {
				continue
			}
			filePath := files[randInt(0, len(files)-1)]
			logAction("MODIFY", filePath)
			require.NoError(t, os.WriteFile(filePath, []byte("modified_"+randString(20)), 0644))
			expectedPaths[filePath] = "MODIFY"
		case 1: // Rename File
			if len(files) == 0 {
				continue
			}
			index := randInt(0, len(files)-1)
			originalPath := files[index]
			newPath := filepath.Join(filepath.Dir(originalPath), "renamed_"+randString(8)+".mv")
			logAction("RENAME", fmt.Sprintf("%s -> %s", originalPath, newPath))
			require.NoError(t, os.Rename(originalPath, newPath))

			// Events for both the original and the new path
			expectedPaths[originalPath] = "RENAME"
			expectedPaths[newPath] = "RENAME"
			files[index] = newPath
		case 2: // Delete File
			if len(files) == 0 {
				continue
			}
			fileIdx := randInt(0, len(files)-1)
			filePath := files[fileIdx]
			logAction("DELETE", filePath)
			require.NoError(t, os.Remove(filePath))
			expectedPaths[filePath] = "DELETE"
			files = append(files[:fileIdx], files[fileIdx+1:]...)
		case 3: // Delete Directory
			if len(dirs) <= 1 {
				continue
			}
			dirIdx := randInt(1, len(dirs)-1)
			dirToDelete := dirs[dirIdx]
			logAction("DELETE D", dirToDelete)

			_ = filepath.Walk(dirToDelete, func(path string, info os.FileInfo, err error) error {
				if err == nil {
					expectedPaths[path] = "DELETE D"
				}
				return nil
			})

			require.NoError(t, os.RemoveAll(dirToDelete))

			// Prune state
			isChild := func(parent, child string) bool {
				rel, err := filepath.Rel(parent, child)
				return err == nil && !strings.HasPrefix(rel, "..")
			}
			var remainingFiles []string
			for _, f := range files {
				if !isChild(dirToDelete, f) {
					remainingFiles = append(remainingFiles, f)
				}
			}
			files = remainingFiles
			var remainingDirs []string
			for _, d := range dirs {
				if !isChild(dirToDelete, d) && d != dirToDelete {
					remainingDirs = append(remainingDirs, d)
				}
			}
			dirs = remainingDirs
		}
	}

	// Ignored files
	if len(dirs) > 0 {
		parentDir := dirs[randInt(0, len(dirs)-1)]
		ignoredPath := filepath.Join(parentDir, "ignored_"+randString(8)+".tmp")
		logAction("IGNORE", ignoredPath)
		require.NoError(t, os.WriteFile(ignoredPath, []byte("ignored"), 0644))
	}
	return expectedPaths
}
