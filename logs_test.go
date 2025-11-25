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

// TestLogSeverity_String tests LogSeverity string
func TestLogSeverity_String(t *testing.T) {
	testCases := []struct {
		level    LogSeverity
		expected string
	}{
		{SeverityError, "ERROR"},
		{SeverityWarn, "WARN"},
		{SeverityInfo, "INFO"},
		{SeverityDebug, "DEBUG"},
		{LogSeverity(99), "UNKNOWN"}, // Edge case
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.level.String())
		})
	}
}

// TestLogger_OutputRouting tests logs on stdout/stderr
func TestLogger_OutputRouting(t *testing.T) {
	w := &watcher{path: "/test/path", severity: SeverityDebug}

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

		expected := fmt.Sprintf("%s [%s] info message", SeverityInfo.Emoji(), base)
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

		expected := fmt.Sprintf("%s [%s] error message", SeverityError.Emoji(), base)
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

		expected := fmt.Sprintf("%s [%s] warn message", SeverityWarn.Emoji(), base)
		assert.Contains(t, buf.String(), expected)
	})
}

// TestLogger_LevelFiltering tests messages filtered by level
func TestLogger_LevelFiltering(t *testing.T) {
	mw := &mockWriter{}
	w := &watcher{
		path:     "/test/path",
		severity: SeverityInfo, // Set level to INFO
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

	expected := fmt.Sprintf("%s [%s] this should be logged", SeverityInfo.Emoji(), base)
	assert.Contains(t, messages[0], expected)
}

// TestLogger_FileLogging tests logs written on file logger
func TestLogger_FileLogging(t *testing.T) {
	t.Run("WithLogFile", func(t *testing.T) {
		// Custom logger
		mw := &mockWriter{}
		w := &watcher{
			path:     "/test/path",
			severity: SeverityDebug,
			logger:   log.New(mw, "PREFIX: ", 0),
		}
		base := filepath.Base(w.path)

		// Capture stdout and stderr
		origStdout := os.Stdout
		rStdout, wStdout, _ := os.Pipe()
		os.Stdout = wStdout

		origStderr := os.Stderr
		rStderr, wStderr, _ := os.Pipe()
		os.Stderr = wStderr

		// Log messages
		w.logInfo("message for file")
		w.logError("error for file")

		// Capture their output
		wStdout.Close()
		os.Stdout = origStdout
		var stdoutBuf bytes.Buffer
		_, _ = io.Copy(&stdoutBuf, rStdout)

		wStderr.Close()
		os.Stderr = origStderr
		var stderrBuf bytes.Buffer
		_, _ = io.Copy(&stderrBuf, rStderr)

		messages := mw.getMessages()
		assert.Equal(t, 2, len(messages), "should log two messages")

		// Check custom logger output
		assert.Contains(t, messages[0], "PREFIX: ")
		expectedInfo := fmt.Sprintf("%s [%s] message for file", SeverityInfo.Emoji(), base)
		assert.Contains(t, messages[0], expectedInfo)

		assert.Contains(t, messages[1], "PREFIX: ")
		expectedError := fmt.Sprintf("%s [%s] error for file", SeverityError.Emoji(), base)
		assert.Contains(t, messages[1], expectedError)

		// Check stdout and stderr
		assert.Empty(t, stdoutBuf.String(), "stdout should be empty when custom logger is used")
		assert.Empty(t, stderrBuf.String(), "stderr should be empty when custom logger is used")
	})
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
		severity: SeverityDebug,
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
