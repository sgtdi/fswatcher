//go:build (darwin && !cgo) || freebsd || openbsd || netbsd || dragonfly

package fswatcher

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// kqueue is the kernel queue-based implementation for macOS (without CGO) and BSD
type kqueue struct {
	w     *watcher
	kqFd  int             // file descriptor
	wds   map[int]string  // watch descriptors to paths
	paths map[string]int  // paths to watch descriptors
	dirs  map[string]bool // directories list
	mu    sync.Mutex
}

// newKqueue creates a new instance of kqueue
func newKqueue(w *watcher) (*kqueue, error) {
	kqFd, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	return &kqueue{
		w:     w,
		kqFd:  kqFd,
		wds:   make(map[int]string),
		paths: make(map[string]int),
		dirs:  make(map[string]bool),
	}, nil
}

// startPlatform initializes the kqueue backend
func (w *watcher) startPlatform(ctx context.Context) (<-chan struct{}, error) {
	kq, err := newKqueue(w)
	if err != nil {
		return nil, fmt.Errorf("kqueue init failed: %w", err)
	}

	w.streams = make(map[string]any)
	w.streams["platform"] = kq

	done := make(chan struct{})

	go w.runKqueueLoop(ctx, kq, done)
	return done, nil
}

// runKqueueLoop starts the event loop
func (w *watcher) runKqueueLoop(ctx context.Context, k *kqueue, done chan struct{}) {
	// Closes all open descriptors and kqueue instance
	defer func() {
		w.lockPlatformShutdown()
		defer w.opsMu.Unlock()
		w.logDebug("kqueue platform shutting down")
		k.mu.Lock()
		for fd := range k.wds {
			unix.Close(fd)
		}
		unix.Close(k.kqFd)
		k.wds = nil
		k.paths = nil
		k.dirs = nil
		k.mu.Unlock()
		close(done)
	}()

	// Init event buffer and timeout
	events := make([]unix.Kevent_t, w.bufferSize)
	ts := unix.NsecToTimespec(w.cooldown.Nanoseconds())
	timeout := &ts
	backoff := newBackoffState()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Polling
		n, err := unix.Kevent(k.kqFd, nil, events, timeout)
		if err != nil {
			// Interrupted signal
			if err == unix.EINTR {
				continue
			}

			if !w.handleLoopError("kqueue", err, backoff) {
				return
			}
			continue
		}

		// Success, reset backoff
		w.resetBackoff(backoff)

		// No events occurred, loop back
		if n == 0 {
			continue
		}

		// Include the 'n' events returned
		w.processKqueueEvents(k, events[:n])
	}
}

// processKqueueEvents handles events from the kqueue
func (w *watcher) processKqueueEvents(k *kqueue, events []unix.Kevent_t) {
	k.mu.Lock()
	defer k.mu.Unlock()

	dirsToScan := make(map[string]struct{})

	for _, event := range events {
		fd := int(event.Ident)
		path, ok := k.wds[fd]
		if !ok {
			continue
		}

		flags := event.Fflags
		if flags&(unix.NOTE_DELETE|unix.NOTE_RENAME) != 0 {
			w.hiddenAttributeCache.invalidateAll()
		}
		// Map kqueue events flags
		var types []EventType
		if flags&unix.NOTE_DELETE == unix.NOTE_DELETE {
			types = append(types, EventRemove)
		}
		if flags&unix.NOTE_RENAME == unix.NOTE_RENAME {
			types = append(types, EventRename)
		}
		if flags&unix.NOTE_ATTRIB == unix.NOTE_ATTRIB {
			types = append(types, EventChmod)
		}
		if flags&unix.NOTE_WRITE == unix.NOTE_WRITE || flags&unix.NOTE_EXTEND == unix.NOTE_EXTEND {
			if k.dirs[path] {
				// Top-level watches need direct-child descriptors so directory
				// entries can be detected, but must not scan those child dirs.
				if w.shouldScanKqueueDirectory(path) {
					dirsToScan[path] = struct{}{}
				}
			} else {
				types = append(types, EventMod)
			}
		}

		// Emit the event
		if len(types) > 0 {
			w.handlePlatformEvent(WatchEvent{
				Path:  path,
				Types: uniqueEventTypes(types),
				Time:  time.Now(),
			})
		}

		// Clean up if removed
		if flags&unix.NOTE_DELETE == unix.NOTE_DELETE || flags&unix.NOTE_RENAME == unix.NOTE_RENAME {
			removeKqueuePathLocked(k, path)
		}
	}

	// Scan directories that had changes (deduplicated)
	for dirPath := range dirsToScan {
		w.scanDirectoryLocked(k, dirPath)
	}
}

func (w *watcher) shouldScanKqueueDirectory(path string) bool {
	watchPath := w.findMostSpecificWatchPath(path)
	return watchPath == nil || watchPath.Depth != WatchTopLevel || filepath.Clean(path) == filepath.Clean(watchPath.Path)
}

// scanDirectoryLocked checks for new files in a watched dir
func (w *watcher) scanDirectoryLocked(k *kqueue, dirPath string) {
	w.hiddenAttributeCache.invalidateAll()
	if !k.w.includeHidden {
		watchRoot := dirPath
		if parentWatch := k.w.findMostSpecificWatchPath(dirPath); parentWatch != nil {
			watchRoot = parentWatch.Path
		}
		if w.isHiddenPathWithin(dirPath, watchRoot) {
			return
		}
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}

	var newEntries []struct {
		path  string
		isDir bool
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dirPath, entry.Name())
		if !k.w.includeHidden && k.w.isHiddenPathWithin(fullPath, fullPath) {
			continue
		}
		if _, exists := k.paths[fullPath]; !exists {
			newEntries = append(newEntries, struct {
				path  string
				isDir bool
			}{fullPath, entry.IsDir()})
		}
	}

	if len(newEntries) > 0 {
		if err := k.batchAddWatchesLocked(newEntries); err == nil {
			w.logInfo("Detected and added new entries", "count", len(newEntries), "dir", dirPath)
			now := time.Now()
			for _, entry := range newEntries {
				// Emit create only for entries that were successfully registered
				if _, exists := k.paths[entry.path]; !exists {
					continue
				}
				w.handlePlatformEvent(WatchEvent{
					Path:  entry.path,
					Types: []EventType{EventCreate},
					Time:  now,
				})
			}

			// kqueue watches directories by descriptor and doesn't inherit a
			// parent's watch for descendants. Populate a newly discovered tree
			// explicitly so files already present in a moved-in directory are
			// monitored immediately.
			if watchPath := k.w.findMostSpecificWatchPath(dirPath); watchPath != nil && watchPath.Depth == WatchNested {
				for _, entry := range newEntries {
					if entry.isDir {
						if _, watched := k.paths[entry.path]; watched {
							if err := k.addWatchContentsLocked(entry.path); err != nil {
								w.logWarn("Failed to add watches for new directory contents", "path", entry.path, "error", err)
							}
						}
					}
				}
			}
		} else {
			w.logWarn("Failed to add watches for new entries", "error", err)
		}
	}
}

// addWatchContentsLocked registers descendants of a directory that is already
// watched. The caller must hold k.mu.
func (k *kqueue) addWatchContentsLocked(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		if !k.w.includeHidden && k.w.isHiddenPathWithin(path, path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if k.w.isIgnoredPattern(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return k.addSingleWatchLocked(path)
	})
}

func removeKqueuePathLocked(k *kqueue, path string) {
	cleanPath := filepath.Clean(path)
	for watchedPath, fd := range k.paths {
		if watchedPath == cleanPath || isSubpath(cleanPath, watchedPath) {
			delete(k.wds, fd)
			delete(k.paths, watchedPath)
			delete(k.dirs, watchedPath)
			_ = unix.Close(fd)
		}
	}
}

// batchAddWatchesLocked adds multiple paths to the kqueue in one syscall
func (k *kqueue) batchAddWatchesLocked(entries []struct {
	path  string
	isDir bool
}) error {
	if len(entries) == 0 {
		return nil
	}

	changes := make([]unix.Kevent_t, 0, len(entries))
	// Keep track of what we successfully opened to update maps later
	added := make([]struct {
		fd    int
		path  string
		isDir bool
	}, 0, len(entries))

	for _, e := range entries {
		// double check existence in case of race or duplicates in input
		if _, exists := k.paths[e.path]; exists {
			continue
		}

		if k.w.isIgnoredPattern(e.path) {
			continue
		}

		fd, err := unix.Open(e.path, kqueueOpenFlags, 0)
		if err != nil {
			// Try O_RDONLY
			if kqueueOpenFlags != unix.O_RDONLY {
				fd, err = unix.Open(e.path, unix.O_RDONLY, 0)
			}
			if err != nil {
				if err == unix.EMFILE || err == unix.ENFILE {
					k.w.logWarn("kqueue: reached open file limit (ulimit -n), consider increasing system limits", "path", e.path)
				}
				continue // Skip unreadable files
			}
		}

		changes = append(changes, unix.Kevent_t{
			Ident:  kqueueIdent(fd),
			Filter: unix.EVFILT_VNODE,
			Flags:  unix.EV_ADD | unix.EV_CLEAR,
			Fflags: unix.NOTE_WRITE | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_ATTRIB | unix.NOTE_EXTEND,
		})
		added = append(added, struct {
			fd    int
			path  string
			isDir bool
		}{fd, e.path, e.isDir})
	}

	if len(changes) == 0 {
		return nil
	}

	// Register all in one syscall
	if _, err := unix.Kevent(k.kqFd, changes, nil, nil); err != nil {
		// If batch registration fails, close all FDs we just opened
		for _, a := range added {
			unix.Close(a.fd)
		}
		return err
	}

	// Update internal state
	for _, a := range added {
		k.wds[a.fd] = a.path
		k.paths[a.path] = a.fd
		if a.isDir {
			k.dirs[a.path] = true
		}
	}

	return nil
}

// addWatch starts a recursive watch
func (k *kqueue) addWatch(watchPath *WatchPath) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	return filepath.WalkDir(watchPath.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if !k.w.includeHidden && k.w.isHiddenPathWithin(path, path) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := k.addSingleWatchLocked(path); err != nil {
			return err
		}
		if watchPath.Depth == WatchTopLevel && path != watchPath.Path && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
}

// addSingleWatchLocked adds a file/dir
func (k *kqueue) addSingleWatchLocked(path string) error {
	if _, exists := k.paths[path]; exists {
		return nil
	}

	// Filter system files
	if k.w.isIgnoredPattern(path) {
		return nil
	}

	fd, err := unix.Open(path, kqueueOpenFlags, 0)
	if err != nil {
		// Try O_RDONLY
		if kqueueOpenFlags != unix.O_RDONLY {
			fd, err = unix.Open(path, unix.O_RDONLY, 0)
		}
		if err != nil {
			if err == unix.EMFILE || err == unix.ENFILE {
				k.w.logWarn("kqueue: reached open file limit (ulimit -n), consider increasing system limits", "path", path)
			}
			return err
		}
	}
	events := []unix.Kevent_t{
		{
			Ident:  kqueueIdent(fd),
			Filter: unix.EVFILT_VNODE,
			Flags:  unix.EV_ADD | unix.EV_CLEAR,
			Fflags: unix.NOTE_WRITE | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_ATTRIB | unix.NOTE_EXTEND,
		},
	}

	if _, err := unix.Kevent(k.kqFd, events, nil, nil); err != nil {
		unix.Close(fd)
		return err
	}

	k.wds[fd] = path
	k.paths[path] = fd

	fi, err := os.Stat(path)
	if err == nil && fi.IsDir() {
		k.dirs[path] = true
	}

	return nil
}

// removeWatch stops watching a given path
func (k *kqueue) removeWatch(path string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	path = filepath.Clean(path)
	for watched, fd := range k.paths {
		if (watched == path || isSubpath(path, watched)) && !k.w.nativeWatchNeeded(watched, path, true) {
			delete(k.wds, fd)
			delete(k.paths, watched)
			delete(k.dirs, watched)
			_ = unix.Close(fd)
		}
	}
	return nil
}
