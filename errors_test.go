package fswatcher

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewError tests the newError function
func TestNewError(t *testing.T) {
	op := "test_op"
	path := "/test/path"
	err := errors.New("test_err")
	watcherErr := newError(op, path, err)

	require.NotNil(t, watcherErr, "newError should not return nil")
	assert.Equal(t, op, watcherErr.Op, "The 'Op' field should be set correctly")
	assert.Equal(t, path, watcherErr.Path, "The 'Path' field should be set correctly")
	assert.Equal(t, err, watcherErr.Err, "The 'Err' field should be set correctly")
}

// TestWatcherError_Error tests the formatting of the error message
func TestWatcherError_Error(t *testing.T) {
	baseErr := errors.New("underlying cause")

	t.Run("With Path", func(t *testing.T) {
		// Arrange
		op := "read_dir"
		path := "/tmp/some/path"
		watcherErr := newError(op, path, baseErr)
		expectedString := fmt.Sprintf("watcher %s %s: %v", op, path, baseErr)
		actualString := watcherErr.Error()
		assert.Equal(t, expectedString, actualString, "Error message should be correctly formatted when a path is present")
	})

	t.Run("Without Path", func(t *testing.T) {
		op := "start_stream"
		watcherErr := newError(op, "", baseErr)
		expectedString := fmt.Sprintf("watcher %s: %v", op, baseErr)
		actualString := watcherErr.Error()
		assert.Equal(t, expectedString, actualString, "Error message should be correctly formatted when a path is empty")
	})

	t.Run("With Nil Error", func(t *testing.T) {
		op := "cleanup"
		path := "/tmp/cache"
		// handle nil gracefully
		watcherErr := newError(op, path, nil)
		expectedString := fmt.Sprintf("watcher %s %s: <nil>", op, path)
		actualString := watcherErr.Error()
		assert.Equal(t, expectedString, actualString, "Error message should handle a nil underlying error")
	})
}

// TestWatcherError_Unwrap tests the unwrapping
func TestWatcherError_Unwrap(t *testing.T) {
	baseErr := errors.New("specific underlying error")
	watcherErr := newError("validate", "/path", baseErr)

	// Test raw Unwrap()
	t.Run("Direct Unwrap", func(t *testing.T) {
		unwrappedErr := watcherErr.Unwrap()
		assert.Equal(t, baseErr, unwrappedErr, "Unwrap() should return the exact original error instance")
	})

	// Test with errors.Is()
	t.Run("Compatibility with errors.Is", func(t *testing.T) {
		// Recursively calls Unwrap() to check for error equality
		assert.True(t, errors.Is(watcherErr, baseErr), "errors.Is() should be able to find the wrapped base error")

		// Create another error to ensure it doesn't match
		otherErr := errors.New("a different error")
		assert.False(t, errors.Is(watcherErr, otherErr), "errors.Is() should not match an unrelated error")
	})

	// Test with errors.As()
	t.Run("Compatibility with errors.As", func(t *testing.T) {
		var targetErr *WatcherError
		isWatcherError := errors.As(watcherErr, &targetErr)

		assert.True(t, isWatcherError, "errors.As() should successfully find a WatcherError type in the chain")
		assert.Equal(t, watcherErr.Op, targetErr.Op, "The target error found by errors.As() should have the correct fields")
		assert.Equal(t, watcherErr.Path, targetErr.Path)
	})
}
