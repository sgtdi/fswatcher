package fswatcher

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// WatcherOpt is a function that configures a watcher instance
type WatcherOpt func(*watcher)

// WithCooldown sets the debouncing cooldown period, events on the same path arriving within this duration will be merged
func WithCooldown(d time.Duration) WatcherOpt {
	return func(w *watcher) {
		w.cooldown = d
	}
}

// WithBufferSize sets the size of the main event channel
func WithBufferSize(size int) WatcherOpt {
	return func(w *watcher) {
		w.bufferSize = size
	}
}

// WithIncRegex sets the regex patterns for paths to include
// If no patterns are provided, all non-excluded paths are included.
func WithIncRegex(patterns []string) WatcherOpt {
	return func(w *watcher) {
		w.incRegexPatterns = patterns
	}
}

// WithExcRegex sets the regex patterns for paths to exclude; exclusions take precedence over inclusions
func WithExcRegex(patterns []string) WatcherOpt {
	return func(w *watcher) {
		w.excRegexPatterns = patterns
	}
}

// WithEventBatching enables and configures event batching; multiple events for the same path within the duration are merged into one
func WithEventBatching(duration time.Duration) WatcherOpt {
	return func(w *watcher) {
		w.batchDuration = duration
	}
}

// WithCustomChannels allows providing external channels for events
func WithCustomChannels(events chan WatchEvent, dropped chan WatchEvent) WatcherOpt {
	return func(w *watcher) {
		w.events = events
		w.dropped = dropped
		w.ownsEventsChannel = false
	}
}

// WithReadyChannel provides a channel that is closed when the watcher is ready
func WithReadyChannel(ready chan struct{}) WatcherOpt {
	return func(w *watcher) {
		w.readyChan = ready
	}
}

// WithLogFile sets a file for logging; if a path is empty, logging is disabled; if "stdout", logs to standard output
func WithLogFile(path string) WatcherOpt {
	return func(w *watcher) {
		if path == "" {
			w.logger = nil
			w.logFile = nil
			return
		}
		if path == "stdout" {
			w.logger = log.New(os.Stdout, "", log.LstdFlags)
			return
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			w.logError("fswatcher: failed to open log file %q: %v", path, err)
			return
		}
		w.logFile = file
		w.logger = log.New(file, "", log.LstdFlags)
	}
}

// WithLogLevel sets the logging verbosity (default is LogLevelWarn)
func WithLogLevel(level LogLevel) WatcherOpt {
	return func(w *watcher) {
		w.logLevel = level
	}
}

// WithPath adds an initial path to watch
func WithPath(path string, options ...PathOption) WatcherOpt {
	return func(w *watcher) {
		if w.init != nil {
			return
		}

		// Default to watching nested directories
		watchPath := &WatchPath{Path: path}

		for _, opt := range options {
			opt(watchPath)
		}

		cleanPath, err := validatePath(watchPath.Path)
		if err != nil {
			w.init = newError("validate_path", watchPath.Path, err)
			return
		}
		if info, err := os.Stat(cleanPath); err != nil {
			w.init = newError("access_path", cleanPath, err)
			return
		} else if !info.IsDir() {
			w.init = newError("validate_path", cleanPath, errors.New("path must be a directory"))
			return
		}
		watchPath.Path = cleanPath

		w.paths = append(w.paths, watchPath)
	}
}

// WatchDepth defines how deeply a directory structure should be watched
type WatchDepth int

const (
	WatchNested   = 0 // Watch the directory and all its subdirectories
	WatchTopLevel = 1 // Watch only the top-level directory
)

// WatchPath holds the configuration for a single watched path
type WatchPath struct {
	Path      string
	Depth     WatchDepth
	filter    PathFilter
	eventMask map[EventType]bool
}

// PathOption is a function that configures a WatchPath
type PathOption func(*WatchPath)

// WithDepth sets the watch depth for a specific path
func WithDepth(depth WatchDepth) PathOption {
	return func(p *WatchPath) {
		p.Depth = depth
	}
}

// WithEventMask specifies which event types to listen for on a specific path
func WithEventMask(eventTypes ...EventType) PathOption {
	return func(p *WatchPath) {
		if p.eventMask == nil {
			p.eventMask = make(map[EventType]bool)
		}
		for _, et := range eventTypes {
			p.eventMask[et] = true
		}
	}
}

// validatePath cleans and validates a path string
func validatePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("watch path cannot be empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	return absPath, nil
}

// validateWatchPath creates and validates a WatchPath struct
func validateWatchPath(path string, depth WatchDepth) (*WatchPath, error) {
	cleanPath, err := validatePath(path)
	if err != nil {
		// Wrap the error for context
		return nil, newError("validate_path", path, err)
	}

	info, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, newError("access_path", cleanPath, fmt.Errorf("path does not exist: %w", err))
		}
		return nil, newError("access_path", cleanPath, fmt.Errorf("cannot access path: %w", err))
	}

	if !info.IsDir() {
		return nil, newError("validate_path", cleanPath, errors.New("path must be a directory, not a file"))
	}

	return &WatchPath{Path: cleanPath, Depth: depth}, nil
}

// PlatformLinux specifies which Linux backend to use
type PlatformLinux int

const (
	PlatformInotify PlatformLinux = iota
	PlatformFanotify
)

// WithLinuxPlatform sets a specific backend (fanotify or inotify) on Linux
func WithLinuxPlatform(platform PlatformLinux) WatcherOpt {
	return func(w *watcher) {
		w.platformLinux = platform
	}
}
