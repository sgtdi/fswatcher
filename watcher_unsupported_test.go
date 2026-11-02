//go:build !darwin && !linux && !windows && !freebsd && !openbsd && !netbsd && !dragonfly

package fswatcher

import (
	"context"
	"errors"
	"testing"
)

// TestUnsupportedPlatform tests that a watcher can be made on a platform
// without a backend, and that Watch reports that it is not supported.
func TestUnsupportedPlatform(t *testing.T) {
	w, err := New(WithPath(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = w.Watch(context.Background())
	if !errors.Is(err, ErrUnsupportedPlatform) || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Watch: got %v, want ErrUnsupportedPlatform", err)
	}
}
