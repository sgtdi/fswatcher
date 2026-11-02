package fswatcher

import (
	"errors"
	"fmt"
)

// ErrUnsupportedPlatform is returned by Watch on platforms without a
// native file system notification API, such as js/wasm and plan9.
// It matches [errors.ErrUnsupported].
var ErrUnsupportedPlatform = fmt.Errorf("file system watching is not supported on this platform: %w", errors.ErrUnsupported)

// WatcherError is a structured error for watcher-related failures
type WatcherError struct {
	Op   string
	Path string
	Err  error
}

// newError creates a new WatcherError
func newError(op, path string, err error) *WatcherError {
	return &WatcherError{Op: op, Path: path, Err: err}
}

// Error returns a formatted error string
func (e *WatcherError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("watcher %s %s: %v", e.Op, e.Path, e.Err)
	}
	return fmt.Sprintf("watcher %s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error for error chaining
func (e *WatcherError) Unwrap() error {
	return e.Err
}
