//go:build linux

package fswatcher

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fanotify is the fanotify platform implementation
type fanotify struct {
	fd    int
	paths map[string]struct{}
	mu    sync.RWMutex
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
	return &fanotify{fd: fd, paths: make(map[string]struct{})}, nil
}

// runFanotifyLoop reads fanotify events and forwards them as WatchEvents
func (w *watcher) runFanotifyLoop(ctx context.Context, p *fanotify, done chan struct{}) {
	defer func() {
		w.logDebug("fanotify platform shutting down...")
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

	const bufSize = 64 * 1024
	buf := make([]byte, bufSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, readErr := unix.Read(p.fd, buf)
		if n <= 0 {
			if readErr == unix.EAGAIN || readErr == unix.EWOULDBLOCK {
				time.Sleep(5 * time.Millisecond)
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
			if offset+unix.FAN_EVENT_METADATA_LEN > n {
				break
			}
			meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&buf[offset]))

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
				path, err = w.parseFanotifyInfo(buf[offset+unix.FAN_EVENT_METADATA_LEN : offset+int(meta.Event_len)])
			}

			if err == nil && path != "" {
				w.dispatchFanotifyEvent(path, meta.Mask)
			} else if err != nil {
				w.logDebug("fanotify resolve error: %v", err)
			}

			offset += int(meta.Event_len)
		}
	}
}

// parseFanotifyInfo parses the variable length info records to reconstructing the path
func (w *watcher) parseFanotifyInfo(infoBuf []byte) (string, error) {

	var dirPath string
	var fileName string

	offset := 0
	for offset < len(infoBuf) {
		// Read the header (Type, Pad, Len)
		if len(infoBuf[offset:]) < 4 { // min header size
			break
		}

		infoType := uint8(infoBuf[offset])
		infoLen := binary.LittleEndian.Uint16(infoBuf[offset+2:])

		if int(infoLen) > len(infoBuf[offset:]) || infoLen < 4 {
			break
		}

		record := infoBuf[offset : offset+int(infoLen)]

		// Check for FAN_EVENT_INFO_TYPE_DFID_NAME or FAN_EVENT_INFO_TYPE_FID
		if infoType == unix.FAN_EVENT_INFO_TYPE_DFID_NAME || infoType == unix.FAN_EVENT_INFO_TYPE_FID || infoType == unix.FAN_EVENT_INFO_TYPE_DFID {
			// Header is 4 bytes, fsid is 8 bytes
			const headerSize = 4
			const fsidSize = 8

			if len(record) < headerSize+fsidSize {
				offset += int(infoLen)
				continue
			}

			handleBytes := record[headerSize+fsidSize:]
			if len(handleBytes) < 8 { // sizeof(uint32) * 2
				offset += int(infoLen)
				continue
			}

			// Parse the C struct into Go's unix.FileHandle
			fHandleBytes := binary.LittleEndian.Uint32(handleBytes[0:4])
			fHandleType := int32(binary.LittleEndian.Uint32(handleBytes[4:8]))

			if len(handleBytes) < 8+int(fHandleBytes) {
				offset += int(infoLen)
				continue
			}

			opaqueHandle := handleBytes[8 : 8+int(fHandleBytes)]
			fileHandle := unix.NewFileHandle(fHandleType, opaqueHandle)

			fd, err := unix.OpenByHandleAt(unix.AT_FDCWD, fileHandle, unix.O_RDONLY|unix.O_PATH)
			if err == nil {
				// Recover path from FD
				dirPath, _ = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
				unix.Close(fd)
			} else {
				w.logDebug("OpenByHandleAt failed: %v", err)
			}
			if infoType == unix.FAN_EVENT_INFO_TYPE_DFID_NAME {
				// Opaque bytes = 4 + 4 + fHandleBytes
				handleStructSize := 4 + 4 + int(fHandleBytes)

				if len(handleBytes) >= handleStructSize {
					// Name starts after the handle struct
					nameBytes := handleBytes[handleStructSize:]
					nullIdx := bytes.IndexByte(nameBytes, 0)
					if nullIdx >= 0 {
						fileName = string(nameBytes[:nullIdx])
					}
				}
			}
		}

		offset += int(infoLen)
	}

	if dirPath != "" {
		if fileName != "" {
			return filepath.Join(dirPath, fileName), nil
		}
		return dirPath, nil
	}

	return "", fmt.Errorf("could not resolve path from handle")
}

func (w *watcher) dispatchFanotifyEvent(path string, mask uint64) {
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

	// FAN_MARK_FILESYSTEM couk be better for full coverage but requires mount point
	flags := uint(unix.FAN_MARK_ADD | unix.FAN_MARK_ONLYDIR)

	// Check if the path exists before marking
	if err := unix.FanotifyMark(p.fd, flags, mask, unix.AT_FDCWD, path); err != nil {
		return newError("createMarkFa", path, err)
	}

	p.mu.Lock()
	p.paths[path] = struct{}{}
	p.mu.Unlock()
	return nil
}

func (p *fanotify) removeWatch(path string) error {
	if err := unix.FanotifyMark(p.fd, uint(unix.FAN_MARK_REMOVE|unix.FAN_MARK_ONLYDIR), ^uint64(0), unix.AT_FDCWD, path); err != nil {
		return newError("removeWatch", path, err)
	}
	p.mu.Lock()
	delete(p.paths, path)
	p.mu.Unlock()
	return nil
}
