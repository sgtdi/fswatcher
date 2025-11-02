package fswatcher

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLogLevel_String tests LogLevel string
func TestLogLevel_String(t *testing.T) {
	testCases := []struct {
		level    LogLevel
		expected string
	}{
		{LogLevelError, "ERROR"},
		{LogLevelWarn, "WARN"},
		{LogLevelInfo, "INFO"},
		{LogLevelDebug, "DEBUG"},
		{LogLevel(99), "UNKNOWN"}, // Edge case
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.level.String())
		})
	}
}

// TestLogger_OutputRouting tests logs on stdout/stderr
func TestLogger_OutputRouting(t *testing.T) {
	w := &watcher{path: "/test/path", logLevel: LogLevelDebug}

	base := filepath.Base(w.path) // "path"

	t.Run("Info logs go to stdout", func(t *testing.T) {
		orig := os.Stdout
		r, wFile, _ := os.Pipe()
		os.Stdout = wFile

		w.logInfo("info message")

		_ = wFile.Close()
		os.Stdout = orig
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		expected := fmt.Sprintf("%s [%s] info message", LogLevelInfo.Emoji(), base)
		assert.Contains(t, buf.String(), expected)
	})

	t.Run("Error logs go to stderr", func(t *testing.T) {
		orig := os.Stderr
		r, wFile, _ := os.Pipe()
		os.Stderr = wFile

		w.logError("error message")

		_ = wFile.Close()
		os.Stderr = orig
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		expected := fmt.Sprintf("%s [%s] error message", LogLevelError.Emoji(), base)
		assert.Contains(t, buf.String(), expected)
	})

	t.Run("Warn logs go to stderr", func(t *testing.T) {
		orig := os.Stderr
		r, wFile, _ := os.Pipe()
		os.Stderr = wFile

		w.logWarn("warn message")

		_ = wFile.Close()
		os.Stderr = orig
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		expected := fmt.Sprintf("%s [%s] warn message", LogLevelWarn.Emoji(), base)
		assert.Contains(t, buf.String(), expected)
	})
}

// TestLogger_LevelFiltering tests messages filtered by level
func TestLogger_LevelFiltering(t *testing.T) {
	mw := &mockWriter{}
	w := &watcher{
		path:     "/test/path",
		logLevel: LogLevelInfo, // Set level to INFO
		logger:   log.New(mw, "", 0),
	}

	base := filepath.Base(w.path)

	// Redirect stdout to silence it
	orig := os.Stdout
	r, wFile, _ := os.Pipe()
	os.Stdout = wFile

	// Act
	w.logInfo("this should be logged")
	w.logDebug("this should be ignored")

	// Restore and discard stdout
	_ = wFile.Close()
	os.Stdout = orig
	_, _ = io.Copy(io.Discard, r)

	messages := mw.getMessages()
	assert.Equal(t, 1, len(messages))

	expected := fmt.Sprintf("%s [%s] this should be logged", LogLevelInfo.Emoji(), base)
	assert.Contains(t, messages[0], expected)
}

// TestLogger_FileLogging tests logs written on file logger
func TestLogger_FileLogging(t *testing.T) {
	// Redirect stdout to silence it
	orig := os.Stdout
	r, wFile, _ := os.Pipe()
	os.Stdout = wFile

	mw := &mockWriter{}
	w := &watcher{
		path:     "/test/path",
		logLevel: LogLevelDebug,
		logger:   log.New(mw, "PREFIX: ", 0),
	}

	base := filepath.Base(w.path)

	w.logInfo("message for file")

	// Restore and discard stdout
	_ = wFile.Close()
	os.Stdout = orig
	_, _ = io.Copy(io.Discard, r)

	messages := mw.getMessages()
	assert.Equal(t, 1, len(messages))

	assert.Contains(t, messages[0], "PREFIX: ")
	expected := fmt.Sprintf("%s [%s] message for file", LogLevelInfo.Emoji(), base)
	assert.Contains(t, messages[0], expected)
}

// TestLogger_Concurrency tests multiple goroutines log calls without race conditions
func TestLogger_Concurrency(t *testing.T) {
	// Redirect stdout to capture it and prevent it from showing in the CLI
	origStdout := os.Stdout
	r, wFile, _ := os.Pipe()
	os.Stdout = wFile

	mw := &mockWriter{}
	w := &watcher{
		path:     "/test/path",
		logLevel: LogLevelDebug,
		logger:   log.New(mw, "", 0),
	}

	var wg sync.WaitGroup
	numGoroutines := 50
	wg.Add(numGoroutines)

	for i := range numGoroutines {
		go func(i int) {
			defer wg.Done()
			w.logInfo("message from goroutine %d", i)
		}(i)
	}

	wg.Wait()

	// Restore stdout
	_ = wFile.Close()
	os.Stdout = origStdout
	// We can discard the output from the pipe, as we are not testing the console output here.
	_, _ = io.Copy(io.Discard, r)

	messages := mw.getMessages()
	assert.Equal(t, numGoroutines, len(messages))

	for i := range numGoroutines {
		expectedSubstring := fmt.Sprintf("message from goroutine %d", i)
		found := false
		for _, msg := range messages {
			if strings.Contains(msg, expectedSubstring) {
				found = true
				break
			}
		}
		assert.True(t, found, "Expected to find log message for goroutine %d", i)
	}
}
