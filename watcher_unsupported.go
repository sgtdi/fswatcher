//go:build !darwin && !linux && !windows && !freebsd && !openbsd && !netbsd && !dragonfly

package fswatcher

import "context"

// Common temporary and backup files, used by DefaultIgnores
var osPrefixes = []string{"~"}
var osSuffixes = []string{".tmp", ".bak", ".swp", "~"}

// startPlatform reports that there is no backend on this platform, so that
// the package builds everywhere, and Watch returns ErrUnsupportedPlatform
func (w *watcher) startPlatform(ctx context.Context) (<-chan struct{}, error) {
	return nil, ErrUnsupportedPlatform
}

// addWatch reports that there is no backend on this platform
func (w *watcher) addWatch(watchPath *WatchPath) error {
	return ErrUnsupportedPlatform
}

// removeWatch reports that there is no backend on this platform
func (w *watcher) removeWatch(path string) error {
	return ErrUnsupportedPlatform
}
