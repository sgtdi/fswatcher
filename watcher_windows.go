//go:build windows

package fswatcher

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procReadDirectoryChangesW = modkernel32.NewProc("ReadDirectoryChangesW")
)

// readDirectoryChangesW wraps the ReadDirectoryChangesW syscall for IOCP
func readDirectoryChangesW(handle windows.Handle, buf []byte, watchSubtree bool, filter uint32, overlapped *windows.Overlapped) error {
	var bWatchSubtree uint32
	if watchSubtree {
		bWatchSubtree = 1
	}

	ret, _, err := procReadDirectoryChangesW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(bWatchSubtree),
		uintptr(filter),
		0, // lpBytesReturned is NULL for async calls
		uintptr(unsafe.Pointer(overlapped)),
		0, // No completion routine
	)
	if ret == 0 {
		// An error is only returned if the request could not be queued; ERROR_IO_PENDING is not a true error
		if err != windows.ERROR_IO_PENDING {
			return err
		}
	}
	return nil
}

// fileNotifyInformation corresponds to the FILE_NOTIFY_INFORMATION structure in the Windows API
type fileNotifyInformation struct {
	NextEntryOffset uint32
	Action          uint32
	FileNameLength  uint32
	FileName        [1]uint16
}

// Windows system files used by isSystemFile
var osPrefixes = []string{"~", "$"}
var osSuffixes = []string{".tmp", ".bak", ".swp"}

// watchedPath holds the state for a single watched directory on Windows
type watchedPath struct {
	handle     windows.Handle
	path       string
	key        uintptr
	buffer     []byte // buffer must be aligned to a 64-bit boundary
	overlapped windows.Overlapped
}

// startPlatform initializes the Windows watcher backend
func (w *watcher) startPlatform(ctx context.Context) (<-chan struct{}, error) {
	iocp, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 0)
	if err != nil {
		return nil, newError("create_iocp", "", err)
	}

	w.streams = make(map[string]any)
	w.streams["iocp"] = iocp
	w.streams["pathMap"] = make(map[uintptr]*watchedPath)
	w.streams["pathCounter"] = uintptr(1) // Start counter at 1
	done := make(chan struct{})

	// Waits for the context to be canceled
	go func() {
		<-ctx.Done()
		windows.PostQueuedCompletionStatus(iocp, 0, 0, nil)
	}()

	go w.runWindowsLoop(iocp, done)

	if w.readyChan != nil {
		close(w.readyChan)
	}

	return done, nil
}

// runWindowsLoop is the main event processing loop for Windows
func (w *watcher) runWindowsLoop(iocp windows.Handle, done chan struct{}) {
	defer func() {
		w.logDebug("Windows platform shutting down...")
		windows.Close(iocp)
		close(done)
	}()

	for {
		var bytesRead uint32
		var key uintptr
		var overlapped *windows.Overlapped

		err := windows.GetQueuedCompletionStatus(iocp, &bytesRead, &key, &overlapped, windows.INFINITE)

		// A completion packet with a null overlapped signal shutdown
		if overlapped == nil {
			return
		}

		if err != nil {
			// ERROR_OPERATION_ABORTED is expected if a handle is closed while a read is pending
			if err == windows.ERROR_OPERATION_ABORTED {
				continue
			}
			w.logError("GetQueuedCompletionStatus error: %v", err)
			continue
		}

		w.streamMu.Lock()
		pathMap := w.streams["pathMap"].(map[uintptr]*watchedPath)
		wp, ok := pathMap[key]
		w.streamMu.Unlock()

		if !ok {
			w.logWarn("Received completion for unknown key: %d", key)
			continue
		}

		if bytesRead > 0 {
			w.handleWindowsEvents(wp, bytesRead)
		}

		// Reissue the read request to continue watching.
		if err := w.readChanges(wp); err != nil {
			// Happen if the directory is deleted
			if err != windows.ERROR_INVALID_HANDLE {
				w.logError("Failed to reissue read for %s: %v", wp.path, err)
			}
		}
	}
}

// handleWindowsEvents parses the buffer from ReadDirectoryChangesW
func (w *watcher) handleWindowsEvents(wp *watchedPath, bytesRead uint32) {
	now := time.Now()
	offset := uint32(0)

	for {
		if offset >= bytesRead {
			break
		}

		raw := (*fileNotifyInformation)(unsafe.Pointer(&wp.buffer[offset]))

		utf16path := unsafe.Slice((*uint16)(unsafe.Pointer(&raw.FileName)), raw.FileNameLength/2)
		path := windows.UTF16ToString(utf16path)
		fullPath := filepath.Join(wp.path, path)

		types := mapWindowsEvents(raw.Action)
		if len(types) > 0 {
			event := WatchEvent{
				Path:  fullPath,
				Types: types,
				Time:  now,
			}
			w.handlePlatformEvent(event)
		}

		if raw.NextEntryOffset == 0 {
			break
		}
		offset += raw.NextEntryOffset
	}
}

// addWatch starts watching a directory on Windows
func (w *watcher) addWatch(watchPath *WatchPath) error {
	w.streamMu.Lock()
	defer w.streamMu.Unlock()

	iocp := w.streams["iocp"].(windows.Handle)
	pathMap := w.streams["pathMap"].(map[uintptr]*watchedPath)
	key := w.streams["pathCounter"].(uintptr)
	w.streams["pathCounter"] = key + 1

	path := watchPath.Path
	if _, exists := w.streams[path]; exists {
		return fmt.Errorf("path %s is already being monitored", path)
	}

	utf16Path, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return newError("convert_path", path, err)
	}
	handle, err := windows.CreateFile(
		utf16Path,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return newError("create_handle", path, err)
	}

	wp := &watchedPath{
		handle: handle,
		path:   path,
		key:    key,
		buffer: make([]byte, w.bufferSize),
	}

	if _, err := windows.CreateIoCompletionPort(handle, iocp, key, 0); err != nil {
		windows.Close(handle)
		return newError("associate_iocp", path, err)
	}

	if err := w.readChanges(wp); err != nil {
		windows.Close(handle)
		return newError("initial_read", path, err)
	}

	w.streams[path] = wp
	pathMap[key] = wp
	w.logInfo("Added watch for %s", path)
	return nil
}

// removeWatch stops watching a directory on Windows
func (w *watcher) removeWatch(path string) error {
	w.streamMu.Lock()
	defer w.streamMu.Unlock()

	wpAny, exists := w.streams[path]
	if !exists {
		return fmt.Errorf("path %s is not being monitored", path)
	}
	wp := wpAny.(*watchedPath)
	pathMap := w.streams["pathMap"].(map[uintptr]*watchedPath)

	// Try to cancel any pending I/O operations
	if err := windows.CancelIoEx(wp.handle, &wp.overlapped); err != nil && err != windows.ERROR_NOT_FOUND {
		w.logWarn("Failed to cancel I/O for %s: %v", path, err)
		// If we can't cancel the I/O, the state is uncertain abort
		return err
	}

	// Close the handle
	closeErr := windows.Close(wp.handle)
	if closeErr != nil {
		w.logWarn("Failed to close handle for %s: %v", path, closeErr)
	}

	// Clean up internal state
	delete(pathMap, wp.key)
	delete(w.streams, path)
	w.logInfo("Removed watch for %s", path)

	return closeErr
}

// readChanges issues an asynchronous ReadDirectoryChangesW call
func (w *watcher) readChanges(wp *watchedPath) error {
	return readDirectoryChangesW(
		wp.handle,
		wp.buffer,
		true, // Watch subtrees
		windows.FILE_NOTIFY_CHANGE_FILE_NAME|
			windows.FILE_NOTIFY_CHANGE_DIR_NAME|
			windows.FILE_NOTIFY_CHANGE_ATTRIBUTES|
			windows.FILE_NOTIFY_CHANGE_SIZE|
			windows.FILE_NOTIFY_CHANGE_LAST_WRITE,
		&wp.overlapped,
	)
}

// mapWindowsEvents translates Windows event flags to cross-platform EventTypes
func mapWindowsEvents(action uint32) []EventType {
	var types []EventType
	switch action {
	case windows.FILE_ACTION_ADDED:
		types = append(types, EventCreate)
	case windows.FILE_ACTION_REMOVED:
		types = append(types, EventRemove)
	case windows.FILE_ACTION_MODIFIED:
		types = append(types, EventMod)
	case windows.FILE_ACTION_RENAMED_OLD_NAME:
		types = append(types, EventRename)
	case windows.FILE_ACTION_RENAMED_NEW_NAME:
		types = append(types, EventRename)
	}
	return types
}
