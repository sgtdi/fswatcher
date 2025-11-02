//go:build linux

package fswatcher

import (
	"context"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fanotify is the fanotify platform implementation
type fanotify struct {
	fd    int
	paths map[string]struct{} // for cleanup
	mu    sync.RWMutex        // guards paths
}

// newFanotify tries to initialize fanotify
func newFanotify() (*fanotify, error) {
	initFlags := uint(unix.FAN_CLASS_NOTIF | unix.FAN_NONBLOCK)
	eventFFlags := uint(unix.O_RDONLY | unix.O_LARGEFILE | unix.O_CLOEXEC)
	fd, err := unix.FanotifyInit(initFlags, eventFFlags)
	if err != nil {
		return nil, err
	}
	return &fanotify{fd: fd, paths: make(map[string]struct{})}, nil
}

// runFanotifyLoop reads fanotify events and forwards them as WatchEvents
func (w *watcher) runFanotifyLoop(ctx context.Context, p *fanotify, done chan struct{}) {
	defer func() {
		w.logDebug("fanotify platform shutting down...")

		// Copy paths and unmark outside the lock
		p.mu.RLock()
		var ps []string
		for path := range p.paths {
			ps = append(ps, path)
		}
		p.mu.RUnlock()

		for _, path := range ps {
			_ = unix.FanotifyMark(p.fd, uint(unix.FAN_MARK_REMOVE|unix.FAN_MARK_ONLYDIR), ^uint64(0), unix.AT_FDCWD, path)
		}

		p.mu.Lock()
		p.paths = make(map[string]struct{})
		p.mu.Unlock()

		_ = unix.Close(p.fd)
		close(done)
	}()

	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, readErr := unix.Read(p.fd, buf)
		if n <= 0 {
			if readErr == unix.EAGAIN || readErr == unix.EWOULDBLOCK {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if readErr != nil {
				w.logError("fanotify read error: %v", readErr)
			}
			return
		}

		offset := 0
		for offset < n {
			raw := buf[offset:]
			if len(raw) < unix.FAN_EVENT_METADATA_LEN {
				break
			}
			meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&raw[0]))
			if meta.Event_len < unix.FAN_EVENT_METADATA_LEN {
				break
			}

			if meta.Event_len <= 0 || int(meta.Event_len) > len(raw) {
				break
			}
			offset += int(meta.Event_len)
		}
	}
}

// addWatch adds a directory mark to fanotify
func (p *fanotify) addWatch(_ *watcher, watchPath *WatchPath) error {
	path := watchPath.Path
	mask := uint64(
		unix.FAN_CREATE |
			unix.FAN_DELETE |
			unix.FAN_MODIFY |
			unix.FAN_MOVED_FROM |
			unix.FAN_MOVED_TO |
			unix.FAN_CLOSE_WRITE |
			unix.FAN_ATTRIB |
			unix.FAN_DELETE_SELF |
			unix.FAN_MOVE_SELF |
			unix.FAN_EVENT_ON_CHILD,
	)

	flags := uint(unix.FAN_MARK_ADD | unix.FAN_MARK_ONLYDIR)
	if err := unix.FanotifyMark(p.fd, flags, mask, unix.AT_FDCWD, path); err != nil {
		return newError("create_mark_fa", path, err)
	}

	p.mu.Lock()
	p.paths[path] = struct{}{}
	p.mu.Unlock()
	return nil
}

func (p *fanotify) removeWatch(path string) error {
	if err := unix.FanotifyMark(p.fd, uint(unix.FAN_MARK_REMOVE|unix.FAN_MARK_ONLYDIR), ^uint64(0), unix.AT_FDCWD, path); err != nil {
		return newError("remove_watch", path, err)
	}
	p.mu.Lock()
	delete(p.paths, path)
	p.mu.Unlock()
	return nil
}

// maskInfo holds the value and name for a fanotify event mask
type maskInfo struct {
	value uint64
	name  string
}

var knownMasks = []maskInfo{
	{unix.FAN_CREATE, "Create"},
	{unix.FAN_DELETE, "Delete"},
	{unix.FAN_MODIFY, "Modify"},
	{unix.FAN_MOVED_FROM, "MovedFrom"},
	{unix.FAN_MOVED_TO, "MovedTo"},
	{unix.FAN_CLOSE_WRITE, "CloseWrite"},
	{unix.FAN_ATTRIB, "Attrib"},
	{unix.FAN_DELETE_SELF, "DeleteSelf"},
	{unix.FAN_MOVE_SELF, "MoveSelf"},
	{unix.FAN_ONDIR, "OnDir"},
	{unix.FAN_Q_OVERFLOW, "QueueOverflow"},
	{unix.FAN_EVENT_ON_CHILD, "OnChild"},
}
