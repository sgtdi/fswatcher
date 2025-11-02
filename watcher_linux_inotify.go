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
type inotify struct {
	fd   int                // inotify fd
	wds  map[int]*WatchPath // wd -> WatchPath config mapping
	trie *pathTrieNode      // Trie for efficient path prefix lookups
	mu   sync.RWMutex       // guards wds and trie
}

// newInotify creates and initializes a new inotify instance
func newInotify() (*inotify, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &inotify{
		fd:   fd,
		wds:  make(map[int]*WatchPath),
		trie: newPathTrieNode(),
	}, nil
}

// runInotifyLoop reads inotify events using epoll for efficiency and forwards them as WatchEvents
func (w *watcher) runInotifyLoop(ctx context.Context, p *inotify, done chan struct{}) {
	defer func() {
		w.logDebug("inotify platform shutting down...")
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

	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		w.logError("inotify epoll_create error: %v", err)
		return
	}
	defer unix.Close(epfd)

	event := unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(p.fd),
	}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, p.fd, &event); err != nil {
		w.logError("inotify epoll_ctl error: %v", err)
		return
	}

	buf := make([]byte, w.bufferSize)
	epollEvents := make([]unix.EpollEvent, 1)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nEvents, err := unix.EpollWait(epfd, epollEvents, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			w.logError("inotify epoll_wait error: %v", err)
			return
		}

		if nEvents == 0 {
			continue
		}

		n, readErr := unix.Read(p.fd, buf)
		if n <= 0 {
			if ctx.Err() != nil {
				return
			}
			if readErr != nil && readErr != unix.EAGAIN {
				w.logError("inotify read error: %v", readErr)
			}
			continue
		}

		now := time.Now()
		offset := 0
		for offset < n {
			if n-offset < unix.SizeofInotifyEvent {
				break
			}
			e := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))

			if e.Mask&unix.IN_Q_OVERFLOW != 0 {
				w.logError("inotify event queue overflowed")
				offset += unix.SizeofInotifyEvent
				continue
			}

			offset += unix.SizeofInotifyEvent

			var name string
			if e.Len > 0 {
				nb := buf[offset : offset+int(e.Len)]
				nullIndex := bytes.IndexByte(nb, 0)
				if nullIndex != -1 {
					name = string(nb[:nullIndex])
				}
				offset += int(e.Len)
			}

			p.mu.RLock()
			parentWatch, ok := p.wds[int(e.Wd)]
			p.mu.RUnlock()
			if !ok {
				continue
			}

			path := parentWatch.Path
			if name != "" {
				path = filepath.Join(parentWatch.Path, name)
			}

			if e.Mask&unix.IN_IGNORED != 0 {
				p.mu.Lock()
				if ignoredPath, pathOk := p.wds[int(e.Wd)]; pathOk {
					delete(p.wds, int(e.Wd))
					if node := p.trie.findNode(ignoredPath.Path); node != nil {
						node.wd = -1
					}
					p.mu.Unlock()

					w.handlePlatformEvent(WatchEvent{
						ID:    0,
						Path:  ignoredPath.Path,
						Types: []EventType{EventRemove},
						Flags: []string{},
						Time:  now,
					})
				} else {
					p.mu.Unlock()
				}
				continue
			}

			if (e.Mask & (unix.IN_DELETE_SELF | unix.IN_MOVE_SELF)) != 0 {
				p.mu.Lock()
				delete(p.wds, int(e.Wd))
				if node := p.trie.findNode(path); node != nil {
					node.wd = -1
				}
				p.mu.Unlock()
			}

			isDir := (e.Mask & unix.IN_ISDIR) != 0

			if isDir && ((e.Mask&unix.IN_CREATE) != 0 || (e.Mask&unix.IN_MOVED_TO) != 0) {
				// Only watch new subdirectories if the parent is not a top-level-only watch
				if parentWatch.Depth != WatchTopLevel {
					// Events for existing children are not emitted
					p.addWatch(w, &WatchPath{Path: path, Depth: WatchNested})
				}
			}

			if isDir && ((e.Mask&unix.IN_DELETE) != 0 || (e.Mask&unix.IN_MOVED_FROM) != 0) {
				var childWds []int
				p.mu.RLock()
				if node := p.trie.findNode(path); node != nil {
					for _, childNode := range node.children {
						childWds = append(childWds, childNode.findAllChildren()...)
					}
				}
				p.mu.RUnlock()

				for _, wd := range childWds {
					p.mu.RLock()
					childPath, ok := p.wds[wd]
					p.mu.RUnlock()
					if ok {
						w.handlePlatformEvent(WatchEvent{
							ID:    0,
							Path:  childPath.Path,
							Types: []EventType{EventRemove},
							Flags: []string{"Synthetic"},
							Time:  now,
						})
					}
				}
			}

			types := mapInotifyMask(e.Mask)
			if len(types) == 0 || path == "" || isSystemFile(path) {
				continue
			}

			w.handlePlatformEvent(WatchEvent{
				ID:    uint64(e.Cookie),
				Path:  path,
				Types: types,
				Flags: []string{},
				Time:  now,
			})
		}
	}
}

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

func (p *inotify) addWatch(w *watcher, watchPath *WatchPath) error {
	path := watchPath.Path
	p.mu.RLock()
	if node := p.trie.findNode(path); node != nil && node.wd != -1 {
		p.mu.RUnlock()
		return fmt.Errorf("path %s is already being monitored", path)
	}
	p.mu.RUnlock()

	wd, err := unix.InotifyAddWatch(p.fd, path, inotifyMask())
	if err != nil {
		return newError("create_watch", path, err)
	}
	p.mu.Lock()
	p.wds[wd] = watchPath
	p.trie.insert(path, wd)
	p.mu.Unlock()

	if watchPath.Depth == WatchTopLevel {
		w.logDebug("Watching top-level only for %s", path)
		return nil
	}

	return filepath.WalkDir(path, func(subp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() || subp == path {
			return nil
		}
		wd2, err := unix.InotifyAddWatch(p.fd, subp, inotifyMask())
		if err != nil {
			if errors.Is(err, unix.ENOSPC) {
				w.logWarn("inotify watch limit reached at %s", subp)
				return fs.SkipDir
			}
			return nil
		}
		p.mu.Lock()
		// Sub-watches are always nested
		p.wds[wd2] = &WatchPath{Path: subp, Depth: WatchNested}
		p.trie.insert(subp, wd2)
		p.mu.Unlock()
		return nil
	})
}

func (p *inotify) removeWatch(path string) error {
	p.mu.RLock()
	node := p.trie.findNode(path)
	p.mu.RUnlock()

	if node == nil {
		return newError("remove_watch", path, errors.New("watch not found for path"))
	}

	wdsToRemove := node.findAllChildren()
	if len(wdsToRemove) == 0 {
		return newError("remove_watch", path, errors.New("watch not found for path"))
	}

	for _, wd := range wdsToRemove {
		_, _ = unix.InotifyRmWatch(p.fd, uint32(wd))
	}

	return nil
}

func isSubpath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !startsWithDotDot(rel)
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}
