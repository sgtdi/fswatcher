package fswatcher

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatcherOptions(t *testing.T) {

	t.Run("WithCooldown", func(t *testing.T) {
		w := &watcher{}
		expectedCooldown := 123 * time.Millisecond
		opt := WithCooldown(expectedCooldown)

		opt(w)

		assert.Equal(t, expectedCooldown, w.cooldown)
	})

	t.Run("WithBufferSize", func(t *testing.T) {
		w := &watcher{}
		expectedSize := 4096
		opt := WithBufferSize(expectedSize)

		opt(w)

		assert.Equal(t, expectedSize, w.bufferSize)
	})

	t.Run("WithIncRegex", func(t *testing.T) {
		w := &watcher{}
		expectedPatterns := []string{`\.go$`, `\.mod$`}
		opt := WithIncRegex(expectedPatterns)

		opt(w)

		assert.Equal(t, expectedPatterns, w.incRegexPatterns)
	})

	t.Run("WithExcRegex", func(t *testing.T) {
		w := &watcher{}
		expectedPatterns := []string{`_test\.go$`}
		opt := WithExcRegex(expectedPatterns)

		opt(w)

		assert.Equal(t, expectedPatterns, w.excRegexPatterns)
	})

	t.Run("WithEventBatching", func(t *testing.T) {
		w := &watcher{}
		expectedDuration := 250 * time.Millisecond
		opt := WithEventBatching(expectedDuration)

		opt(w)

		assert.Equal(t, expectedDuration, w.batchDuration)
	})

	t.Run("WithCustomChannels", func(t *testing.T) {
		w := &watcher{ownsEventsChannel: true} // Start with the default
		eventsChan := make(chan WatchEvent)
		droppedChan := make(chan WatchEvent)
		opt := WithCustomChannels(eventsChan, droppedChan)

		opt(w)

		assert.Equal(t, eventsChan, w.events, "Events channel should be set to the custom one")
		assert.Equal(t, droppedChan, w.dropped, "Dropped channel should be set to the custom one")
		assert.False(t, w.ownsEventsChannel, "ownsEventsChannel should be set to false")
	})

	t.Run("WithReadyChannel", func(t *testing.T) {
		w := &watcher{}
		readyChan := make(chan struct{})
		opt := WithReadyChannel(readyChan)

		opt(w)

		assert.Equal(t, readyChan, w.readyChan)
	})

	t.Run("WithLogLevel", func(t *testing.T) {
		w := &watcher{}
		expectedLevel := LogLevelError
		opt := WithLogLevel(expectedLevel)

		opt(w)

		assert.Equal(t, expectedLevel, w.logLevel)
	})

	t.Run("WithLogFile", func(t *testing.T) {
		t.Run("with empty path disables logger", func(t *testing.T) {
			w := &watcher{logger: log.Default()}
			opt := WithLogFile("")

			opt(w)

			assert.Nil(t, w.logger)
			assert.Nil(t, w.logFile)
		})

		t.Run("with stdout path", func(t *testing.T) {
			w := &watcher{}
			opt := WithLogFile("stdout")

			opt(w)

			require.NotNil(t, w.logger)
			assert.Nil(t, w.logFile, "logFile handle should be nil for stdout")
		})

		t.Run("with a valid file path", func(t *testing.T) {
			tempDir := t.TempDir()
			logPath := filepath.Join(tempDir, "test.log")
			w := &watcher{}
			opt := WithLogFile(logPath)

			opt(w)
			if w.logFile != nil {
				defer func() { _ = w.logFile.Close() }()
			}

			require.NotNil(t, w.logger, "Logger should be configured")
			require.NotNil(t, w.logFile, "logFile handle should be created")
			assert.Equal(t, logPath, w.logFile.Name(), "The file name should match the provided path")
		})

		t.Run("with an invalid file path", func(t *testing.T) {
			// Path invalid cause the directory doesn't exist
			invalidPath := "/nonexistent/directory/test.log"

			// Redirect stderr to silence the console output
			origStderr := os.Stderr
			r, wFile, _ := os.Pipe()
			os.Stderr = wFile

			mw := &mockWriter{}
			w := &watcher{
				logger:   log.New(mw, "", 0),
				logLevel: LogLevelDebug,
			}
			opt := WithLogFile(invalidPath)

			opt(w)

			// Restore stderr and discard its output
			_ = wFile.Close()
			os.Stderr = origStderr
			_, _ = io.Copy(io.Discard, r)

			assert.NotNil(t, w.logger, "Logger should not be changed on failure")
			assert.Nil(t, w.logFile, "logFile handle should not be created on failure")

			messages := mw.getMessages()
			require.Len(t, messages, 1, "Expected one log message")
			assert.Contains(t, messages[0], "failed to open log file")
		})
	})

	t.Run("WithPath", func(t *testing.T) {
		t.Run("with a valid directory", func(t *testing.T) {
			tempDir := t.TempDir()
			w := &watcher{}
			opt := WithPath(tempDir)

			opt(w)

			require.Nil(t, w.init)
			require.Len(t, w.paths, 1)

			absPath, err := filepath.Abs(tempDir)
			require.NoError(t, err)

			assert.Equal(t, absPath, w.paths[0].Path)
			assert.Equal(t, WatchDepth(WatchNested), w.paths[0].Depth, "Default depth should be WatchNested")
			assert.Nil(t, w.paths[0].filter)
			assert.Nil(t, w.paths[0].eventMask)
		})

		t.Run("with options", func(t *testing.T) {
			tempDir := t.TempDir()
			w := &watcher{}
			// Assuming these event types are defined in the package
			eventTypes := []EventType{EventCreate, EventRemove}

			opt := WithPath(tempDir,
				WithDepth(WatchTopLevel),
				WithEventMask(eventTypes...),
			)

			opt(w)

			require.Nil(t, w.init)
			require.Len(t, w.paths, 1)

			watchPath := w.paths[0]
			absPath, err := filepath.Abs(tempDir)
			require.NoError(t, err)

			assert.Equal(t, absPath, watchPath.Path)
			assert.Equal(t, WatchDepth(WatchTopLevel), watchPath.Depth)

			require.NotNil(t, watchPath.eventMask)
			assert.Len(t, watchPath.eventMask, 2)
			assert.True(t, watchPath.eventMask[EventCreate])
			assert.True(t, watchPath.eventMask[EventRemove])
		})

		t.Run("with a non-existent path", func(t *testing.T) {
			w := &watcher{}
			opt := WithPath("/path/to/non/existent/dir")

			opt(w)

			assert.NotNil(t, w.init, "init error should be set for non-existent path")
			assert.Len(t, w.paths, 0, "No path should be added on failure")
		})

		t.Run("with a file path", func(t *testing.T) {
			tempDir := t.TempDir()
			filePath := filepath.Join(tempDir, "file.txt")
			f, err := os.Create(filePath)
			require.NoError(t, err)
			_ = f.Close()

			w := &watcher{}
			opt := WithPath(filePath)

			opt(w)

			assert.NotNil(t, w.init, "init error should be set for a file path")
			assert.Len(t, w.paths, 0, "No path should be added on failure")
		})

		t.Run("with an empty path", func(t *testing.T) {
			w := &watcher{}
			opt := WithPath("")

			opt(w)

			assert.NotNil(t, w.init, "init error should be set for an empty path")
			assert.Len(t, w.paths, 0, "No path should be added on failure")
		})
	})

}
