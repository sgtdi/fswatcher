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
func WithIncRegex(patterns ...string) WatcherOpt {
	return func(w *watcher) {
		w.incRegexPatterns = patterns
	}
}

// WithExcRegex sets the regex patterns for paths to exclude; exclusions take precedence over inclusions
func WithExcRegex(patterns ...string) WatcherOpt {
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

// WithSeverity sets the logging verbosity (default is SeverityWarn)
func WithSeverity(level Severity) WatcherOpt {
	return func(w *watcher) {
		w.severity = level
	}
}

// WithPath adds an initial path to watch
func WithPath(path string, options ...PathOption) WatcherOpt {
	return func(w *watcher) {
		if w.init != nil {
			return
		}

		// Create a temporary WatchPath to apply options and get depth
		tempWp := &WatchPath{Path: path, Depth: WatchNested}
		for _, opt := range options {
			opt(tempWp)
		}

		// Validate the path using the new centralized function
		wp, err := validateWatchPath(path, tempWp.Depth)
		if err != nil {
			w.init = err
			return
		}

		// Preserve other options like filter and event mask that were set on tempWp
		wp.filter = tempWp.filter
		wp.eventMask = tempWp.eventMask

		w.paths = append(w.paths, wp)
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

// validateWatchPath creates and validates a WatchPath struct
func validateWatchPath(path string, depth WatchDepth) (*WatchPath, error) {
	if path == "" {
		return nil, newError("ValidateWatchPath", "", errors.New("path cannot be empty"))
	}

	var cleanPath string
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, newError("ValidateWatchPath", path, fmt.Errorf("failed to stat path: %w", err))
		}

		// Path does not exist, try from working directory
		cwd, getwdErr := os.Getwd()
		if getwdErr != nil {
			return nil, newError("ValidateWatchPath", "", fmt.Errorf("could not get working directory: %w", getwdErr))
		}
		cleanPath = filepath.Join(cwd, path)

		info, err = os.Stat(cleanPath)
		if err != nil {
			return nil, newError("ValidateWatchPath", path, errors.New("path does not exist as provided or relative to working directory"))
		}
	} else {
		cleanPath = path
	}

	if !info.IsDir() {
		return nil, newError("ValidateWatchPath", cleanPath, errors.New("path is not a directory"))
	}

	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return nil, newError("ValidateWatchPath", cleanPath, fmt.Errorf("failed to get absolute path: %w", err))
	}

	return &WatchPath{Path: absPath, Depth: depth}, nil
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
