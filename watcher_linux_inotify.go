//go:build linux

package fswatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pathTrieNode represents a node in our prefix tree for watched paths
type pathTrieNode struct {
	children map[string]*pathTrieNode
	wd       int    // Watch descriptor, non-zero if this node represents a watched path
	path     string // Full path, stored for convenience
}

// newPathTrieNode creates a new, empty trie node
func newPathTrieNode() *pathTrieNode {
	return &pathTrieNode{
		children: make(map[string]*pathTrieNode),
		wd:       -1, // Use -1 to indicate no watch descriptor is associated with this node
	}
}

// insert adds a path to the trie, creating nodes as needed
func (n *pathTrieNode) insert(path string, wd int) {
	node := n
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if _, ok := node.children[part]; !ok {
			node.children[part] = newPathTrieNode()
		}
		node = node.children[part]
	}
	node.wd = wd
	node.path = path
}

// findNode traverses the trie to find the node corresponding to a path
func (n *pathTrieNode) findNode(path string) *pathTrieNode {
	node := n
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, ok := node.children[part]
		if !ok {
			return nil // Path not found
		}
		node = child
	}
	return node
}

// findAllChildren performs a DFS from a given node to find all descendant watches
func (n *pathTrieNode) findAllChildren() []int {
	var wds []int
	if n == nil {
		return wds
	}

	var queue []*pathTrieNode
	queue = append(queue, n)

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		if node.wd != -1 {
			wds = append(wds, node.wd)
		}
		for _, child := range node.children {
			queue = append(queue, child)
		}
	}
	return wds
}

// inotify is the inotify platform implementation
type inotifyMove struct {
	path        string
	descriptors map[int]struct{}
	deadline    time.Time
}

const inotifyMoveGrace = 100 * time.Millisecond

type inotify struct {
	moves map[uint32]inotifyMove
	w     *watcher
	fd    int                // inotify fd
	wds   map[int]*WatchPath // wd -> WatchPath config mapping
	trie  *pathTrieNode      // Trie for efficient path prefix lookups
	mu    sync.RWMutex       // guards wds and trie
}

// newInotify creates and initializes a new inotify instance
func newInotify() (*inotify, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &inotify{
		fd:    fd,
		wds:   make(map[int]*WatchPath),
		trie:  newPathTrieNode(),
		moves: make(map[uint32]inotifyMove),
	}, nil
}

// setupEpoll creates an epoll instance and adds the inotify and eventfd
func setupEpoll(inotifyFD, eventFD int) (int, error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("setupEpoll error: %w", err)
	}

	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, inotifyFD, &unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(inotifyFD)}); err != nil {
		unix.Close(epfd)
		return -1, fmt.Errorf("epollCtl add inotify error: %w", err)
	}

	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, eventFD, &unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(eventFD)}); err != nil {
		unix.Close(epfd)
		return -1, fmt.Errorf("epollCtl add eventfd error: %w", err)
	}

	return epfd, nil
}

// runInotifyLoop reads inotify events using epoll for efficiency and forwards them as WatchEvents
func (w *watcher) runInotifyLoop(ctx context.Context, p *inotify, done chan struct{}) {
	defer func() {
		w.lockPlatformShutdown()
		defer w.opsMu.Unlock()
		w.logDebug("inotify platform shutting down")
		p.mu.RLock()
		var wds []int
		for wd := range p.wds {
			wds = append(wds, wd)
		}
		p.mu.RUnlock()

		for _, wd := range wds {
			_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
		}
		p.mu.Lock()
		p.wds = make(map[int]*WatchPath)
		p.trie = newPathTrieNode()
		p.mu.Unlock()

		_ = unix.Close(p.fd)
		close(done)
	}()

	eventFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		w.logError("inotify eventfd error", "error", err)
		return
	}
	defer unix.Close(eventFD)

	epfd, err := setupEpoll(p.fd, eventFD)
	if err != nil {
		w.logError("inotify setup error", "error", err)
		return
	}
	defer unix.Close(epfd)

	go func() {
		<-ctx.Done()
		var val uint64 = 1
		_, _ = unix.Write(eventFD, (*(*[8]byte)(unsafe.Pointer(&val)))[:])
	}()

	buf := make([]byte, w.readBufferSize)
	epollEvents := make([]unix.EpollEvent, 2) // Waiting on two FDs: inotify and eventfd
	backoff := newBackoffState()

	for {
		nEvents, err := unix.EpollWait(epfd, epollEvents, p.moveTimeout(time.Now()))
		if err != nil {
			if err == unix.EINTR {
				continue
			}

			if !w.handleLoopError("inotify", err, backoff) {
				return
			}
			continue
		}

		p.expireMoves(time.Now())

		for i := range nEvents {
			if epollEvents[i].Fd == int32(eventFD) {
				// Shutdown signal
				return
			}

			if epollEvents[i].Fd == int32(p.fd) {
				// Inotify event is ready to be proecessed
				n, readErr := unix.Read(p.fd, buf)
				if n <= 0 {
					if readErr == unix.EAGAIN || readErr == unix.EINTR {
						continue
					}
					if readErr == nil {
						readErr = errors.New("inotify read returned no data")
					}
					if !w.handleLoopError("inotify read", readErr, backoff) {
						return
					}
					continue
				}
				w.resetBackoff(backoff)
				w.processInotifyEvents(p, buf[:n])
			}
		}
	}
}

// handleWatchRemovalEvents handles events that signify a watch is no longer valid
func (w *watcher) handleWatchRemovalEvents(p *inotify, e *unix.InotifyEvent, path string) bool {
	if e.Mask&unix.IN_IGNORED != 0 {
		p.mu.Lock()
		if ignoredPath, pathOk := p.wds[int(e.Wd)]; pathOk {
			delete(p.wds, int(e.Wd))
			if node := p.trie.findNode(ignoredPath.Path); node != nil {
				node.wd = -1
			}
		}
		p.mu.Unlock()
		return true
	}

	if e.Mask&unix.IN_DELETE_SELF != 0 {
		p.mu.Lock()
		p.removeSubtreeLocked(path)
		p.mu.Unlock()
	}
	if e.Mask&unix.IN_MOVE_SELF != 0 {
		p.mu.Lock()
		// A watched parent supplies FROM/TO cookies and owns path updates.
		parent := p.trie.findNode(filepath.Dir(path))
		if parent != nil && parent.wd != -1 {
			p.mu.Unlock()
			return true
		}
		p.removeSubtreeLocked(path)
		p.mu.Unlock()
	}

	return false
}

// handleInotifyEvent processes a single inotify event.
func (w *watcher) handleInotifyEvent(p *inotify, e *unix.InotifyEvent, now time.Time, buf []byte, offset int) {
	if e.Mask&unix.IN_Q_OVERFLOW != 0 {
		w.logError("inotify event queue overflowed")
		w.handlePlatformEvent(WatchEvent{Types: []EventType{EventOverflow}, Time: now})
		p.reconcileAfterOverflow(w)
		return
	}

	p.mu.RLock()
	parentWatch, ok := p.wds[int(e.Wd)]
	p.mu.RUnlock()
	if !ok {
		return
	}

	var name string
	if e.Len > 0 {
		nameBytes := buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+int(e.Len)]
		nullIndex := bytes.IndexByte(nameBytes, 0)
		if nullIndex != -1 {
			name = string(nameBytes[:nullIndex])
		}
	}

	path := parentWatch.Path
	if name != "" {
		path = filepath.Join(parentWatch.Path, name)
	}

	if e.Mask&unix.IN_ISDIR != 0 && e.Cookie != 0 {
		if e.Mask&unix.IN_MOVED_FROM != 0 {
			p.beginMove(e.Cookie, path, now)
		}
		if e.Mask&unix.IN_MOVED_TO != 0 {
			p.completeMove(w, e.Cookie, path)
		}
	}

	// Handle events that remove or invalidate the watch first.
	if w.handleWatchRemovalEvents(p, e, path) {
		return
	}

	if p.descriptorInMove(int(e.Wd)) {
		return
	}

	isDir := (e.Mask & unix.IN_ISDIR) != 0
	if isDir && (e.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0) {
		if w.nativeWatchNeeded(path, "", false) {
			if err := p.addWatch(w, &WatchPath{Path: path, Depth: WatchNested}); err != nil {
				w.logWarn("Failed to watch new directory", "path", path, "error", err)
			}
		}
	}

	types := mapInotifyMask(e.Mask)
	if len(types) == 0 || path == "" {
		return
	}

	w.handlePlatformEvent(WatchEvent{
		ID:    uint64(e.Cookie),
		Path:  path,
		Types: types,
		Flags: []string{},
		Time:  now,
	})
}

// processInotifyEvents parses the raw byte buffer from inotify and handles each event.
func (w *watcher) processInotifyEvents(p *inotify, buf []byte) {
	now := time.Now()
	offset := 0
	for offset < len(buf) {
		if len(buf)-offset < unix.SizeofInotifyEvent {
			break
		}
		e := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
		if uint64(e.Len) > uint64(len(buf)-offset-unix.SizeofInotifyEvent) {
			break
		}
		w.handleInotifyEvent(p, e, now, buf, offset)

		eventSize := unix.SizeofInotifyEvent + int(e.Len)
		offset += eventSize
	}
}

// inotifyMask returns the combined inotify event mask for monitoring file system changes
func inotifyMask() uint32 {
	return unix.IN_CREATE |
		unix.IN_DELETE |
		unix.IN_MODIFY |
		unix.IN_CLOSE_WRITE |
		unix.IN_ATTRIB |
		unix.IN_MOVED_FROM |
		unix.IN_MOVED_TO |
		unix.IN_DELETE_SELF |
		unix.IN_MOVE_SELF
}

// mapInotifyMask converts inotify event flags into cross-platform EventType values
func mapInotifyMask(mask uint32) []EventType {
	var evs []EventType
	if mask&unix.IN_CREATE != 0 || mask&unix.IN_MOVED_TO != 0 {
		evs = append(evs, EventCreate)
	}
	if mask&unix.IN_DELETE != 0 || mask&unix.IN_DELETE_SELF != 0 || mask&unix.IN_MOVED_FROM != 0 {
		evs = append(evs, EventRemove)
	}
	if (mask&(unix.IN_MOVED_FROM|unix.IN_MOVED_TO) != 0) || (mask&unix.IN_MOVE_SELF) != 0 {
		evs = append(evs, EventRename)
	}
	if mask&(unix.IN_MODIFY|unix.IN_CLOSE_WRITE) != 0 {
		evs = append(evs, EventMod)
	}
	if mask&unix.IN_ATTRIB != 0 {
		evs = append(evs, EventChmod)
	}
	return uniqueEventTypes(evs)
}

// addWatch attaches a watch to a directory
func (p *inotify) addWatch(w *watcher, watchPath *WatchPath) error {
	p.mu.Lock()
	p.w = w
	p.mu.Unlock()
	path := watchPath.Path
	watchRoot := path
	isWatchRoot := false
	if parentWatch := w.findMostSpecificWatchPath(path); parentWatch != nil {
		watchRoot = parentWatch.Path
		isWatchRoot = filepath.Clean(parentWatch.Path) == filepath.Clean(path)
	}
	hiddenPath := !w.includeHidden && w.isHiddenPathWithin(path, watchRoot)
	if hiddenPath && !isWatchRoot {
		return nil
	}

	wd, err := unix.InotifyAddWatch(p.fd, path, inotifyMask())
	if err != nil {
		return newError("createWatch", path, err)
	}
	p.mu.Lock()
	p.wds[wd] = watchPath
	p.trie.insert(path, wd)
	p.mu.Unlock()

	if watchPath.Depth == WatchTopLevel {
		w.logDebug("Watching top-level only", "path", path)
		return nil
	}
	if hiddenPath {
		w.logDebug("Watching hidden root only", "path", path)
		return nil
	}

	return filepath.WalkDir(path, func(subp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !w.includeHidden && w.isHiddenPathWithin(subp, subp) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() || subp == path {
			return nil
		}
		wd2, err := unix.InotifyAddWatch(p.fd, subp, inotifyMask())
		if err != nil {
			if errors.Is(err, unix.ENOSPC) {
				atomic.AddInt64(&w.stats.eventsPartial, 1)
				w.logWarn("inotify watch limit reached", "path", subp)
				return fs.SkipDir
			}
			return nil
		}
		p.mu.Lock()
		// Sub-watches inherit path-specific filtering from the watched root.
		p.wds[wd2] = &WatchPath{
			Path:      subp,
			Depth:     WatchNested,
			filter:    watchPath.filter,
			eventMask: watchPath.eventMask,
		}
		p.trie.insert(subp, wd2)
		p.mu.Unlock()
		return nil
	})
}

// removeWatch removes inotify watches for a path and all its child paths
func (p *inotify) removeWatch(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for wd, wp := range p.wds {
		if wp == nil || (wp.Path != path && !isSubpath(path, wp.Path)) {
			continue
		}
		if p.w != nil && p.w.nativeWatchNeeded(wp.Path, path, false) {
			continue
		}
		_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
		delete(p.wds, wd)
	}
	p.rebuildTrieLocked()
	return nil
}

func (p *inotify) rebuildTrieLocked() {
	p.trie = newPathTrieNode()
	for wd, wp := range p.wds {
		if wp != nil {
			p.trie.insert(wp.Path, wd)
		}
	}
}

func (p *inotify) removeSubtreeLocked(path string) {
	for wd, wp := range p.wds {
		if wp.Path == path || isSubpath(path, wp.Path) {
			_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
			delete(p.wds, wd)
		}
	}
	p.rebuildTrieLocked()
}

func (p *inotify) beginMove(cookie uint32, path string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	move := inotifyMove{path: path, descriptors: make(map[int]struct{}), deadline: now.Add(inotifyMoveGrace)}
	for wd, wp := range p.wds {
		if wp.Path == path || isSubpath(path, wp.Path) {
			move.descriptors[wd] = struct{}{}
		}
	}
	if p.moves == nil {
		p.moves = make(map[uint32]inotifyMove)
	}
	p.moves[cookie] = move
}

func (p *inotify) completeMove(w *watcher, cookie uint32, destination string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	move, exists := p.moves[cookie]
	if !exists {
		return
	}
	delete(p.moves, cookie)
	for wd := range move.descriptors {
		wp, exists := p.wds[wd]
		if !exists || (wp.Path != move.path && !isSubpath(move.path, wp.Path)) {
			continue
		}
		path := destination + strings.TrimPrefix(wp.Path, move.path)
		if !w.nativeWatchNeeded(path, "", false) || w.isIgnored(path) {
			_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
			delete(p.wds, wd)
			continue
		}
		updated := *wp
		updated.Path = path
		p.wds[wd] = &updated
	}
	p.rebuildTrieLocked()
}

func (p *inotify) descriptorInMove(wd int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, move := range p.moves {
		if _, exists := move.descriptors[wd]; exists {
			return true
		}
	}
	return false
}

func (p *inotify) moveTimeout(now time.Time) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	timeout := -1
	for _, move := range p.moves {
		remaining := max(0, int(move.deadline.Sub(now).Milliseconds())+1)
		if timeout == -1 || remaining < timeout {
			timeout = remaining
		}
	}
	return timeout
}

func (p *inotify) expireMoves(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := false
	for cookie, move := range p.moves {
		if now.Before(move.deadline) {
			continue
		}
		for wd := range move.descriptors {
			wp, exists := p.wds[wd]
			if exists && (wp.Path == move.path || isSubpath(move.path, wp.Path)) {
				_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
				delete(p.wds, wd)
				changed = true
			}
		}
		delete(p.moves, cookie)
	}
	if changed {
		p.rebuildTrieLocked()
	}
}

// A queue overflow can hide creates, deletes and renames. Re-register from
// configured roots instead of trusting descriptor-to-path mappings afterwards.
func (p *inotify) reconcileAfterOverflow(w *watcher) {
	w.opsMu.Lock()
	defer w.opsMu.Unlock()
	if w.isShuttingDown.Load() {
		return
	}
	p.mu.Lock()
	for wd := range p.wds {
		_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
	}
	p.wds = make(map[int]*WatchPath)
	p.trie = newPathTrieNode()
	p.moves = make(map[uint32]inotifyMove)
	p.mu.Unlock()
	w.pathMu.RLock()
	roots := make([]*WatchPath, 0, len(w.watchedPaths))
	for _, root := range w.watchedPaths {
		roots = append(roots, root)
	}
	w.pathMu.RUnlock()
	for _, root := range roots {
		if err := p.addWatch(w, root); err != nil {
			atomic.AddInt64(&w.stats.eventsPartial, 1)
			w.logError("Failed to restore watch after overflow", "path", root.Path, "error", err)
		}
	}
}
