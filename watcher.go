package fswatcher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default values for watcher config
const (
	DefaultCooldown        = 100 * time.Millisecond
	DefaultBufferSize      = 4096
	DefaultCleanupInterval = 5 * time.Minute
	CleanupAge             = 15 * time.Minute
	CleanupThreshold       = 1000
)

// Limits and validation constants
const (
	MinCooldownDuration   = 1 * time.Millisecond
	MaxCooldownDuration   = 10 * time.Second
	MaxBufferSize         = 256 * 1024
	MinDroppedBuffer      = 16
	MaxDroppedBufferRatio = 4
)

// WatcherStats holds runtime statistics
type WatcherStats struct {
	StartTime       time.Time     // StartTime is when the watcher was created
	Uptime          time.Duration // Uptime is the duration the watcher has been running
	EventsProcessed int64         // EventsProcessed is the count of events sent to the Events channel
	EventsDropped   int64         // EventsDropped is the count of events sent to the Dropped channel
	EventsFiltered  int64         // EventsFiltered is the count of events ignored by filters or debouncing
	EventsLost      int64         // EventsLost is the count of events lost when all channels were full
	ProcessingRate  float64       // ProcessingRate is the number of events processed per second
}

// watcherStats holds internal, mutable statistics
type watcherStats struct {
	startTime       time.Time
	eventsProcessed int64
	eventsDropped   int64
	eventsFiltered  int64
	eventsLost      int64
}

// watcher implements the Watcher interface
type watcher struct {
	// Atomic fields
	stats watcherStats

	// Atomic booleans
	isRunning      atomic.Bool
	isShuttingDown atomic.Bool

	// Configuration fields
	init              error
	path              string
	cooldown          time.Duration
	batchDuration     time.Duration
	bufferSize        int
	severity          LogSeverity
	ownsEventsChannel bool
	incRegexPatterns  []string
	excRegexPatterns  []string

	// Linux fields
	platformLinux PlatformLinux

	// Channels
	events    chan WatchEvent
	dropped   chan WatchEvent
	done      chan struct{}
	readyChan chan struct{}

	// Components
	batcher   *eventBatcher
	filter    PathFilter
	debouncer Debouncer

	// Paths
	paths        []*WatchPath
	watchedPaths map[string]*WatchPath
	pathMu       sync.RWMutex

	// Synchronization and logs
	eventWg     sync.WaitGroup
	logMu       sync.Mutex
	logFile     *os.File
	logger      *log.Logger
	proxy       sync.Once
	eventsProxy chan WatchEvent

	// Darwin fields
	handle   uintptr
	streams  map[string]any
	streamMu sync.Mutex
}

// Watcher defines the public interface for the file system watcher
type Watcher interface {
	// Watch starts the watcher and blocks until the context is canceled
	Watch(ctx context.Context) error
	// AddPath adds a new path to watch at runtime
	AddPath(path string, options ...PathOption) error
	// DropPath removes a path from watching at runtime
	DropPath(path string) error
	// Events returns the channel for receiving file system events
	Events() <-chan WatchEvent
	// Dropped returns the channel for receiving dropped events
	Dropped() <-chan WatchEvent
	// IsRunning returns true if the watcher is currently running
	IsRunning() bool
	// Stats returns the current watcher statistics
	Stats() WatcherStats
	// Paths returns a slice of the absolute paths currently being watched
	Paths() []string
	// Close gracefully shuts down the watcher
	Close()
}

// New creates a new Watcher with the given options
func New(options ...WatcherOpt) (Watcher, error) {
	// Create a watcher with default values
	w := &watcher{
		done:              make(chan struct{}),
		cooldown:          DefaultCooldown,
		bufferSize:        DefaultBufferSize,
		ownsEventsChannel: true,
		severity:          SeverityWarn,
		watchedPaths:      make(map[string]*WatchPath),
	}

	// Apply user-provided options
	for _, opt := range options {
		if opt != nil {
			opt(w)
		}
	}

	// Check for any initialization errors from options
	if w.init != nil {
		return nil, w.init
	}

	// If no path is specified, use the current working directory
	if len(w.paths) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, newError("resolveDefaultPath", "", err)
		}
		wp, err := validateWatchPath(cwd, WatchNested)
		if err != nil {
			return nil, err
		}
		w.paths = append(w.paths, wp)
	}

	// Add initial paths to the watchedPaths map
	for _, wp := range w.paths {
		w.watchedPaths[wp.Path] = wp
	}
	w.paths = nil

	// Create channels
	if w.events == nil {
		w.events = make(chan WatchEvent, w.bufferSize)
	}
	if w.dropped == nil {
		// Create a buffer for dropped events
		droppedSize := max(w.bufferSize/MaxDroppedBufferRatio, MinDroppedBuffer)
		w.dropped = make(chan WatchEvent, droppedSize)
	}

	// Initialize internal components
	var includePatterns, excludePatterns []*regexp.Regexp
	if len(w.incRegexPatterns) > 0 {
		var err error
		includePatterns, err = compileRegex(w.incRegexPatterns)
		if err != nil {
			return nil, newError("compileRegex", "include", err)
		}
	}
	if len(w.excRegexPatterns) > 0 {
		var err error
		excludePatterns, err = compileRegex(w.excRegexPatterns)
		if err != nil {
			return nil, newError("compileRegex", "exclude", err)
		}
	}

	w.filter = newPatternFilter(includePatterns, excludePatterns)
	w.debouncer = newDebouncer(w.cooldown)
	if w.batchDuration > 0 {
		w.batcher = newEventBatcher(w, w.batchDuration)
	}
	w.stats.startTime = time.Now()
	return w, nil
}

// compileRegex compiles a slice of string patterns into a slice of *regexp.Regexp
func compileRegex(patterns []string) ([]*regexp.Regexp, error) {
	var compiled []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

// Watch starts the main event loop for the watcher
func (w *watcher) Watch(ctx context.Context) error {
	if err := w.validateConfig(); err != nil {
		return newError("config", "", err)
	}

	if w.severity <= SeverityDebug {
		w.logDebug("Watcher configuration:")
		w.logDebug("  - Cooldown: %v", w.cooldown)
		w.logDebug("  - BufferSize: %d", w.bufferSize)
		w.logDebug("  - BatchDuration: %v", w.batchDuration)
		w.pathMu.RLock()
		paths := make([]string, 0, len(w.watchedPaths))
		for p := range w.watchedPaths {
			paths = append(paths, p)
		}
		w.pathMu.RUnlock()
		w.logDebug("  - WatchedPaths: %v", paths)
	}

	if !w.isRunning.CompareAndSwap(false, true) {
		return newError("start", w.path, errors.New("watcher is already running"))
	}
	defer w.isRunning.Store(false)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-w.done:
		case <-ctx.Done():
		}
		cancel()
	}()

	// Start the debouncer cleanup goroutine
	w.eventWg.Add(1)
	go w.runCleanup(watchCtx)

	// Start the platform-specific watching mechanism
	done, err := w.startPlatform(watchCtx)
	if err != nil {
		return err
	}

	// Start watching the initial set of paths
	w.pathMu.RLock()
	for _, wp := range w.watchedPaths {
		watchPath := wp
		go func() {
			if err := w.addWatch(watchPath); err != nil {
				w.logError("Failed to start watching initial path %q: %v", watchPath.Path, err)
			}
		}()
	}
	w.pathMu.RUnlock()

	// Wait for the context to be canceled
	<-watchCtx.Done()

	// Wait for the platform watcher to shut down completely
	<-done

	w.logInfo("Watcher shutting down...")
	w.isShuttingDown.Store(true)

	if w.batcher != nil {
		w.batcher.close()
	}

	if w.ownsEventsChannel {
		close(w.events)
	}

	w.eventWg.Wait()

	if w.logFile != nil {
		if err := w.logFile.Close(); err != nil {
			log.Printf("fswatcher: error closing log file: %v", err)
		}
	}

	w.logInfo("Watcher shutdown completed")
	return nil
}

// AddPath adds a new directory to watch at runtime
func (w *watcher) AddPath(path string, options ...PathOption) error {
	if !w.IsRunning() {
		return newError("AddPath", path, errors.New("watcher is not running"))
	}

	// Create a temporary WatchPath to apply options
	watchPath := &WatchPath{Path: path, Depth: WatchNested}
	for _, opt := range options {
		opt(watchPath)
	}

	wp, err := validateWatchPath(watchPath.Path, watchPath.Depth)
	if err != nil {
		return err
	}
	// Preserve the configured filter and event mask
	wp.filter = watchPath.filter
	wp.eventMask = watchPath.eventMask

	w.pathMu.Lock()
	defer w.pathMu.Unlock()

	if _, exists := w.watchedPaths[wp.Path]; exists {
		return newError("AddPath", wp.Path, errors.New("path is already being watched"))
	}

	if err := w.addWatch(wp); err != nil {
		return err
	}

	w.watchedPaths[wp.Path] = wp
	w.logInfo("Successfully added new watch path: %s (Depth: %d)", wp.Path, wp.Depth)
	return nil
}

// DropPath stops monitoring a path at runtime
func (w *watcher) DropPath(path string) error {
	if !w.IsRunning() {
		return newError("DropPath", path, errors.New("watcher is not running"))
	}

	wp, err := validateWatchPath(path, WatchNested) // Depth doesn't matter for removal
	if err != nil {
		return err
	}

	w.pathMu.Lock()
	defer w.pathMu.Unlock()

	if _, exists := w.watchedPaths[wp.Path]; !exists {
		return newError("DropPath", path, errors.New("path is not being watched"))
	}

	if err := w.removeWatch(wp.Path); err != nil {
		w.logError("Platform backend failed to remove watch for %s: %v", wp.Path, err)
		// still attempt to remove from our state
	}

	// Remove the path from our internal state
	delete(w.watchedPaths, wp.Path)
	w.logInfo("Successfully removed watch path: %s", wp.Path)
	return nil
}

// Events returns the event channel.
func (w *watcher) Events() <-chan WatchEvent {
	// If logging is not verbose enough to show individual events, return the original channel directly.
	if w.severity < SeverityInfo {
		return w.events
	}

	// Use sync.Once to ensure the proxy channel and goroutine are created only once.
	w.proxy.Do(func() {
		// Create a proxy channel with the same buffer size as the original.
		w.eventsProxy = make(chan WatchEvent, w.bufferSize)

		// Start a single goroutine to safely read from the original channel and forward to the proxy.
		go func() {
			// When the original events channel closes, this loop will terminate, and we close the proxy channel.
			defer close(w.eventsProxy)
			for ev := range w.events {
				// Log each event before forwarding it to the consumer.
				w.logDebug("Raw event: %s", ev.String())
				w.eventsProxy <- ev
			}
		}()
	})

	return w.eventsProxy
}

// Dropped returns the dropped event channel
func (w *watcher) Dropped() <-chan WatchEvent { return w.dropped }

// IsRunning returns the current running state of the watcher
func (w *watcher) IsRunning() bool { return w.isRunning.Load() }

// Stats returns the current watcher statistics
func (w *watcher) Stats() WatcherStats {
	stats := WatcherStats{
		StartTime:       w.stats.startTime,
		EventsProcessed: atomic.LoadInt64(&w.stats.eventsProcessed),
		EventsDropped:   atomic.LoadInt64(&w.stats.eventsDropped),
		EventsFiltered:  atomic.LoadInt64(&w.stats.eventsFiltered),
		EventsLost:      atomic.LoadInt64(&w.stats.eventsLost),
	}

	// Add calculated fields
	uptime := time.Since(stats.StartTime)
	stats.Uptime = uptime

	if uptime.Seconds() > 0 {
		stats.ProcessingRate = float64(stats.EventsProcessed) / uptime.Seconds()
	}

	return stats
}

// Paths returns a slice of the absolute paths currently being watched
func (w *watcher) Paths() []string {
	w.pathMu.RLock()
	defer w.pathMu.RUnlock()

	paths := make([]string, 0, len(w.watchedPaths))
	for path := range w.watchedPaths {
		paths = append(paths, path)
	}
	return paths
}

// Close gracefully shuts down the watcher
func (w *watcher) Close() {
	if w.isShuttingDown.CompareAndSwap(false, true) {
		w.log(SeverityInfo, "Close called, initiating shutdown...")
		close(w.done)
	}
}

// validateConfig checks if the watcher configuration is valid
func (w *watcher) validateConfig() error {
	if w.bufferSize <= 0 || w.bufferSize > MaxBufferSize {
		return fmt.Errorf("buffer size must be between 1 and %d", MaxBufferSize)
	}
	if w.cooldown < 0 || w.cooldown > MaxCooldownDuration {
		return fmt.Errorf("cooldown must be between 0 and %v", MaxCooldownDuration)
	}
	if w.batchDuration < 0 {
		return errors.New("batch duration cannot be negative")
	}
	if w.batchDuration > 0 && w.batchDuration < MinCooldownDuration {
		return fmt.Errorf("batch duration is too small (minimum is %v), or set to 0 to disable", MinCooldownDuration)
	}
	return nil
}

// sendToChannel sends an event to the appropriate channel
func (w *watcher) sendToChannel(event WatchEvent) {
	select {
	case w.events <- event:
		atomic.AddInt64(&w.stats.eventsProcessed, 1)
	default:
		// Main channel is full, try the dropped channel
		select {
		case w.dropped <- event:
			atomic.AddInt64(&w.stats.eventsDropped, 1)
			w.log(SeverityWarn, "Event channel full, event sent to dropped channel: %s", event.String())
		default:
			// Both channels are full, the event is lost
			atomic.AddInt64(&w.stats.eventsLost, 1)
			w.log(SeverityWarn, "Event and dropped channels full, event lost: %s", event.String())
		}
	}
}

// runCleanup periodically cleans the debouncer cache
func (w *watcher) runCleanup(ctx context.Context) {
	defer w.eventWg.Done()
	ticker := time.NewTicker(DefaultCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cleaned := w.debouncer.Cleanup(CleanupAge)
			if cleaned > 0 {
				w.log(SeverityDebug, "Cleaned up %d stale entries from debouncer cache", cleaned)
			}
		case <-ctx.Done():
			return
		}
	}
}

// handlePlatformEvent processes raw events from the OS
func (w *watcher) handlePlatformEvent(event WatchEvent) {
	if w.isShuttingDown.Load() {
		return
	}

	// Platform-agnostic depth filtering
	w.pathMu.RLock()
	var parentWatch *WatchPath
	// Find the base watch path that this event belongs to
	for watchedDir, wp := range w.watchedPaths {
		// Use filepath.Clean to handle trailing slashes consistently
		if strings.HasPrefix(filepath.Clean(event.Path), filepath.Clean(watchedDir)) {
			parentWatch = wp
			break
		}
	}
	w.pathMu.RUnlock()

	// If the parent watch is configured for top-level only, check the depth
	if parentWatch != nil && parentWatch.Depth == WatchTopLevel {
		// Get the directory of the event path
		eventDir := filepath.Dir(event.Path)
		// If the event's directory is not the same as the watched path, it's a subdirectory event so will be ignored
		if filepath.Clean(eventDir) != filepath.Clean(parentWatch.Path) {
			w.log(SeverityDebug, "Filtered by depth: %s", event.Path)
			atomic.AddInt64(&w.stats.eventsFiltered, 1)
			return
		}
	}

	w.logDebug("Received event: %s", event.String())

	// Filter system files
	if isSystemFile(event.Path) {
		w.log(SeverityDebug, "Filtered system file: %s", event.Path)
		atomic.AddInt64(&w.stats.eventsFiltered, 1)
		return
	}

	// Filter patterns
	if !w.filter.ShouldInclude(event.Path) {
		w.log(SeverityDebug, "Filtered by pattern: %s", event.Path)
		atomic.AddInt64(&w.stats.eventsFiltered, 1)
		return
	}

	// Debounce
	merged, ok := w.debouncer.ShouldProcess(event)
	if !ok {
		w.log(SeverityDebug, "Merged by debouncer: %s → %v", merged.Path, merged.Types)
		return
	}

	// Dispatch
	if w.batcher != nil {
		w.log(SeverityDebug, "Dispatching event to batcher: %s", merged.String())
		w.batcher.addEvent(merged)
	} else {
		w.log(SeverityDebug, "Dispatching event directly: %s", merged.String())
		w.sendToChannel(merged)
	}
}
