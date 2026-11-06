//go:build linux

package fswatcher

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const mountCacheMaxSize = 256

// fanotify is the fanotify platform implementation
type fanotify struct {
	fd              int
	paths           map[string]struct{} // configured watch roots, used for handle resolution
	roots           map[string]*fanotifyRoot
	markRefs        map[string]int
	resolveFn       func(uint64, unix.FileHandle) (string, error)
	markFn          func(path string, add bool) error // injectable mark operation for tests
	mountCache      map[uint64]string                 // Cache for FSID to mount path resolution
	mountCacheOrder []uint64                          // FIFO insertion order for eviction
	mu              sync.RWMutex
}

type fanotifyRoot struct {
	depth WatchDepth
	dirs  map[string]struct{}
}

func removeFSIDFromOrder(order []uint64, fsid uint64) []uint64 {
	for i, v := range order {
		if v == fsid {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}

func (p *fanotify) getMountCache(fsid uint64) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	path, exists := p.mountCache[fsid]
	if !exists {
		return "", false
	}

	// Promote recently used FSIDs to the back.
	p.mountCacheOrder = removeFSIDFromOrder(p.mountCacheOrder, fsid)
	p.mountCacheOrder = append(p.mountCacheOrder, fsid)
	return path, true
}

func (p *fanotify) setMountCache(fsid uint64, path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.mountCache[fsid]; exists {
		p.mountCache[fsid] = path
		p.mountCacheOrder = removeFSIDFromOrder(p.mountCacheOrder, fsid)
		p.mountCacheOrder = append(p.mountCacheOrder, fsid)
		return
	}

	if len(p.mountCacheOrder) >= mountCacheMaxSize {
		evicted := p.mountCacheOrder[0]
		p.mountCacheOrder = p.mountCacheOrder[1:]
		delete(p.mountCache, evicted)
	}

	p.mountCache[fsid] = path
	p.mountCacheOrder = append(p.mountCacheOrder, fsid)
}

// newFanotify tries to initialize fanotify with directory monitoring support
func newFanotify() (*fanotify, error) {
	// FAN_REPORT_DFID_NAME is required for FAN_CREATE, FAN_DELETE, etc
	initFlags := uint(unix.FAN_CLASS_NOTIF | unix.FAN_NONBLOCK | unix.FAN_REPORT_DFID_NAME)
	eventFFlags := uint(unix.O_RDONLY | unix.O_LARGEFILE | unix.O_CLOEXEC)

	fd, err := unix.FanotifyInit(initFlags, eventFFlags)
	if err != nil {
		return nil, fmt.Errorf("fanotify init failed: %w", err)
	}
	return &fanotify{
		fd:         fd,
		paths:      make(map[string]struct{}),
		roots:      make(map[string]*fanotifyRoot),
		markRefs:   make(map[string]int),
		mountCache: make(map[uint64]string),
	}, nil
}

// runFanotifyLoop reads fanotify events and forwards them as WatchEvents
func (w *watcher) runFanotifyLoop(ctx context.Context, p *fanotify, done chan struct{}) {
	defer func() {
		w.lockPlatformShutdown()
		defer w.opsMu.Unlock()
		w.logDebug("fanotify platform shutting down...")
		p.closeMarks()

		_ = unix.Close(p.fd)
		close(done)
	}()

	eventFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		w.logError("fanotify eventfd error", "error", err)
		return
	}
	defer unix.Close(eventFD)

	stopWake := startCancellationWakeup(ctx, func() {
		var val uint64 = 1
		_, _ = unix.Write(eventFD, (*(*[8]byte)(unsafe.Pointer(&val)))[:])
	})
	defer stopWake()

	buf := make([]byte, w.readBufferSize)
	backoff := newBackoffState()
	pollFds := []unix.PollFd{
		{Fd: int32(p.fd), Events: unix.POLLIN},
		{Fd: int32(eventFD), Events: unix.POLLIN},
	}

	for {
		_, pollErr := unix.Poll(pollFds, -1)
		if pollErr != nil {
			if pollErr == unix.EINTR {
				continue
			}
			if !w.handleLoopError("fanotify", pollErr, backoff) {
				return
			}
			continue
		}

		if pollFds[1].Revents&unix.POLLIN != 0 {
			return
		}

		if pollFds[0].Revents&(unix.POLLIN|unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) == 0 {
			continue
		}

		n, readErr := unix.Read(p.fd, buf)
		if n <= 0 {
			if readErr == unix.EAGAIN || readErr == unix.EWOULDBLOCK {
				continue
			}
			if ctx.Err() != nil {
				return
			}

			if !w.handleLoopError("fanotify", readErr, backoff) {
				return
			}
			continue
		}

		// Success, reset backoff
		w.resetBackoff(backoff)

		offset := 0
		for offset < n {
			if offset+unix.FAN_EVENT_METADATA_LEN > n {
				break
			}
			meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&buf[offset]))
			if meta.Mask&unix.FAN_Q_OVERFLOW != 0 {
				w.dispatchFanotifyEvent(p, "", meta.Mask)
				offset += int(meta.Event_len)
				continue
			}

			// Check for structural validity
			if meta.Event_len < unix.FAN_EVENT_METADATA_LEN || int(meta.Event_len) > (n-offset) {
				break
			}

			// Process the event
			var path string
			var err error

			if meta.Fd >= 0 {
				// Rarely happen with REPORT_DFID_NAME
				path, err = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", meta.Fd))
				unix.Close(int(meta.Fd))
			} else if meta.Fd == unix.FAN_NOFD {
				// Parse the additional info records to get the FID and Name
				path, err = w.parseFanotifyInfo(p, buf[offset+unix.FAN_EVENT_METADATA_LEN:offset+int(meta.Event_len)])
			}

			if err == nil && path != "" {
				w.dispatchFanotifyEvent(p, path, meta.Mask)
			} else if err != nil {
				w.logDebug("fanotify resolve error", "error", err)
			}

			offset += int(meta.Event_len)
		}
	}
}

// parseFanotifyInfo parses the variable length info records to reconstructing the path
func (w *watcher) parseFanotifyInfo(p *fanotify, buf []byte) (string, error) {
	var fallback string
	for len(buf) > 0 {
		if len(buf) < 4 {
			return "", errors.New("truncated fanotify info header")
		}
		size := int(binary.NativeEndian.Uint16(buf[2:4]))
		if size < 4 || size > len(buf) {
			return "", errors.New("invalid fanotify info length")
		}
		record := buf[:size]
		buf = buf[size:]
		kind := record[0]
		if kind != unix.FAN_EVENT_INFO_TYPE_DFID_NAME && kind != unix.FAN_EVENT_INFO_TYPE_DFID && kind != unix.FAN_EVENT_INFO_TYPE_FID {
			continue
		}
		if len(record) < 20 {
			return "", errors.New("truncated fanotify file handle")
		}
		fsid := binary.NativeEndian.Uint64(record[4:12])
		handleSize := uint64(binary.NativeEndian.Uint32(record[12:16]))
		if handleSize > uint64(len(record)-20) {
			return "", errors.New("invalid fanotify file handle length")
		}
		handleEnd := 20 + int(handleSize)
		handle := unix.NewFileHandle(int32(binary.NativeEndian.Uint32(record[16:20])), record[20:handleEnd])
		// FSID identifies a filesystem, not a directory. Resolve every handle;
		// the mount cache supplies a mount candidate only to OpenByHandleAt.
		path, err := p.resolveHandlePath(fsid, handle)
		if err != nil {
			return "", err
		}
		if kind == unix.FAN_EVENT_INFO_TYPE_DFID_NAME {
			name := record[handleEnd:]
			end := bytes.IndexByte(name, 0)
			if end < 0 {
				return "", errors.New("unterminated fanotify entry name")
			}
			return filepath.Join(path, string(name[:end])), nil
		}
		fallback = path
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("could not resolve path from handle")
}

func (p *fanotify) watchedPathsSnapshot(fsid uint64) []string {
	cached, _ := p.getMountCache(fsid)
	p.mu.RLock()
	defer p.mu.RUnlock()
	paths := make([]string, 0, len(p.paths)+len(p.markRefs)+1)
	seen := make(map[string]bool)
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	add(cached)
	for path := range p.paths {
		add(path)
	}
	for path := range p.markRefs {
		add(path)
	}
	return paths
}

func (p *fanotify) resolveHandlePath(fsid uint64, fileHandle unix.FileHandle) (string, error) {
	if p.resolveFn != nil {
		return p.resolveFn(fsid, fileHandle)
	}
	var lastErr error
	for _, mountPath := range p.watchedPathsSnapshot(fsid) {
		mountFD, err := unix.Open(mountPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			lastErr = err
			continue
		}

		fd, err := unix.OpenByHandleAt(mountFD, fileHandle, unix.O_RDONLY|unix.O_PATH)
		_ = unix.Close(mountFD)
		if err != nil {
			lastErr = err
			continue
		}

		resolved, readErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		_ = unix.Close(fd)
		if readErr != nil {
			lastErr = readErr
			continue
		}
		p.setMountCache(fsid, mountPath)
		return resolved, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no watched mount path available for fsid %d", fsid)
}

func (w *watcher) dispatchFanotifyEvent(p *fanotify, path string, mask uint64) {
	if mask&unix.FAN_Q_OVERFLOW != 0 {
		w.logWarn("fanotify event queue overflowed; some filesystem events were lost")
		w.handlePlatformEvent(WatchEvent{
			Types: []EventType{EventOverflow},
			Time:  time.Now(),
		})
		p.reconcileWatchDirs(w)
		return
	}

	path = filepath.Clean(path)
	if mask&unix.FAN_ONDIR != 0 || mask&(unix.FAN_DELETE_SELF|unix.FAN_MOVE_SELF) != 0 {
		if mask&(unix.FAN_CREATE|unix.FAN_MOVED_TO) != 0 {
			if err := p.addDirectoryTree(w, path); err != nil {
				w.logWarn("Failed to add fanotify marks for new directory tree", "path", path, "error", err)
			}
		}
		if mask&(unix.FAN_DELETE|unix.FAN_MOVED_FROM|unix.FAN_DELETE_SELF|unix.FAN_MOVE_SELF) != 0 {
			if err := p.removeDirectoryTree(path); err != nil {
				w.logWarn("Failed to remove stale fanotify directory marks", "path", path, "error", err)
			}
		}
	}

	var types []EventType
	if mask&unix.FAN_CREATE == unix.FAN_CREATE {
		types = append(types, EventCreate)
	}
	if mask&unix.FAN_DELETE == unix.FAN_DELETE || mask&unix.FAN_DELETE_SELF == unix.FAN_DELETE_SELF {
		types = append(types, EventRemove)
	}
	if mask&unix.FAN_MODIFY == unix.FAN_MODIFY {
		types = append(types, EventMod)
	}
	if mask&unix.FAN_MOVED_FROM == unix.FAN_MOVED_FROM || mask&unix.FAN_MOVED_TO == unix.FAN_MOVED_TO || mask&unix.FAN_MOVE_SELF == unix.FAN_MOVE_SELF {
		types = append(types, EventRename)
	}
	if mask&unix.FAN_ATTRIB == unix.FAN_ATTRIB {
		types = append(types, EventChmod)
	}
	if mask&unix.FAN_CLOSE_WRITE == unix.FAN_CLOSE_WRITE {
		types = append(types, EventMod)
	}

	if len(types) > 0 {
		w.handlePlatformEvent(WatchEvent{
			Path:  path,
			Types: uniqueEventTypes(types),
			Time:  time.Now(),
		})
	}
}

func fanotifyWatchMask() uint64 {
	return uint64(
		unix.FAN_CREATE |
			unix.FAN_DELETE |
			unix.FAN_MODIFY |
			unix.FAN_MOVED_FROM |
			unix.FAN_MOVED_TO |
			unix.FAN_CLOSE_WRITE |
			unix.FAN_ATTRIB |
			unix.FAN_DELETE_SELF |
			unix.FAN_MOVE_SELF |
			unix.FAN_ONDIR |
			unix.FAN_EVENT_ON_CHILD,
	)
}

func (p *fanotify) markDirectory(path string, add bool) error {
	if p.markFn != nil {
		return p.markFn(path, add)
	}
	flags := uint(unix.FAN_MARK_ONLYDIR)
	if add {
		flags |= unix.FAN_MARK_ADD
	} else {
		flags |= unix.FAN_MARK_REMOVE
	}
	return unix.FanotifyMark(p.fd, flags, fanotifyWatchMask(), unix.AT_FDCWD, path)
}

func collectFanotifyDirectories(w *watcher, root string, depth WatchDepth) ([]string, error) {
	dirs := []string{filepath.Clean(root)}
	if depth == WatchTopLevel {
		return dirs, nil
	}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if filepath.Clean(path) == filepath.Clean(root) {
				return err
			}
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Clean(path) == filepath.Clean(root) || !entry.IsDir() {
			return nil
		}
		if !w.includeHidden && w.isHiddenPathWithin(path, root) {
			return fs.SkipDir
		}
		dirs = append(dirs, filepath.Clean(path))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

// addDirectoriesLocked attaches marks not already owned by root. The caller
// must hold p.mu.
func (p *fanotify) addDirectoriesLocked(root *fanotifyRoot, dirs []string) error {
	var added []string
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if _, exists := root.dirs[dir]; exists {
			continue
		}
		if p.markRefs[dir] == 0 {
			if err := p.markDirectory(dir, true); err != nil {
				for i := len(added) - 1; i >= 0; i-- {
					if rollbackErr := p.releaseDirectoryLocked(root, added[i]); rollbackErr != nil {
						err = errors.Join(err, fmt.Errorf("rollback fanotify mark %q: %w", added[i], rollbackErr))
					}
				}
				return err
			}
		}
		p.markRefs[dir]++
		root.dirs[dir] = struct{}{}
		added = append(added, dir)
	}
	return nil
}

func (p *fanotify) releaseDirectoryLocked(root *fanotifyRoot, dir string) error {
	if _, exists := root.dirs[dir]; !exists {
		return nil
	}
	refs := p.markRefs[dir]
	if refs <= 1 {
		if err := p.markDirectory(dir, false); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		delete(p.markRefs, dir)
	} else {
		p.markRefs[dir] = refs - 1
	}
	delete(root.dirs, dir)
	return nil
}

func (p *fanotify) releaseDirectoriesLocked(root *fanotifyRoot, path string) error {
	var firstErr error
	for dir := range root.dirs {
		if dir != path && !isSubpath(path, dir) {
			continue
		}
		if err := p.releaseDirectoryLocked(root, dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *fanotify) addWatch(w *watcher, watchPath *WatchPath) error {
	path := filepath.Clean(watchPath.Path)
	dirs, err := collectFanotifyDirectories(w, path, watchPath.Depth)
	if err != nil {
		return newError("createMarkFa", path, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.roots[path]; exists {
		return fmt.Errorf("path %s is already being monitored", path)
	}
	root := &fanotifyRoot{depth: watchPath.Depth, dirs: make(map[string]struct{})}
	if err := p.addDirectoriesLocked(root, dirs); err != nil {
		return newError("createMarkFa", path, err)
	}
	p.roots[path] = root
	p.paths[path] = struct{}{}
	return nil
}

func (p *fanotify) addDirectoryTree(w *watcher, path string) error {
	if !w.includeHidden {
		if root := w.findMostSpecificWatchPath(path); root != nil && w.isHiddenPathWithin(path, root.Path) {
			return nil
		}
	}
	dirs, err := collectFanotifyDirectories(w, path, WatchNested)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // The new directory may have been removed before it was scanned.
		}
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for rootPath, root := range p.roots {
		if root.depth != WatchNested || (path != rootPath && !isSubpath(rootPath, path)) {
			continue
		}
		if err := p.addDirectoriesLocked(root, dirs); err != nil {
			return fmt.Errorf("mark subtree for %s: %w", rootPath, err)
		}
	}
	return nil
}

func (p *fanotify) removeDirectoryTree(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	for _, root := range p.roots {
		if err := p.releaseDirectoriesLocked(root, path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *fanotify) reconcileWatchDirs(w *watcher) {
	type rootSnapshot struct {
		path  string
		depth WatchDepth
	}
	p.mu.RLock()
	roots := make([]rootSnapshot, 0, len(p.roots))
	for path, root := range p.roots {
		roots = append(roots, rootSnapshot{path: path, depth: root.depth})
	}
	p.mu.RUnlock()

	for _, snapshot := range roots {
		dirs, err := collectFanotifyDirectories(w, snapshot.path, snapshot.depth)
		if err != nil {
			w.logWarn("Failed to rescan fanotify watch root after queue overflow", "path", snapshot.path, "error", err)
			continue
		}
		wanted := make(map[string]struct{}, len(dirs))
		for _, dir := range dirs {
			wanted[dir] = struct{}{}
		}

		p.mu.Lock()
		root, exists := p.roots[snapshot.path]
		if exists {
			for dir := range root.dirs {
				if _, keep := wanted[dir]; !keep {
					if err := p.releaseDirectoryLocked(root, dir); err != nil {
						w.logWarn("Failed to remove stale fanotify mark after queue overflow", "path", dir, "error", err)
					}
				}
			}
			if err := p.addDirectoriesLocked(root, dirs); err != nil {
				w.logWarn("Failed to restore fanotify marks after queue overflow", "path", snapshot.path, "error", err)
			}
		}
		p.mu.Unlock()
	}
}

func (p *fanotify) closeMarks() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for path := range p.markRefs {
		if err := p.markDirectory(path, false); err != nil && !errors.Is(err, unix.ENOENT) {
			// Shutdown is best-effort; closing the fanotify group releases marks.
		}
	}
	p.paths = make(map[string]struct{})
	p.roots = make(map[string]*fanotifyRoot)
	p.markRefs = make(map[string]int)
}

func (p *fanotify) removeWatch(path string) error {
	path = filepath.Clean(path)
	p.mu.Lock()
	defer p.mu.Unlock()
	root, exists := p.roots[path]
	if !exists {
		return nil
	}
	if err := p.releaseDirectoriesLocked(root, path); err != nil {
		return newError("removeWatch", path, err)
	}
	delete(p.roots, path)
	delete(p.paths, path)
	return nil
}
