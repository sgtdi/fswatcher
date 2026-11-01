//go:build windows

package fswatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// Windows system files used by DefaultIgnores
var osPrefixes = []string{"~", "$"}
var osSuffixes = []string{".tmp", ".bak", ".swp"}

// watchedPath holds the state for a single watched directory on Windows
type watchedPath struct {
	handle     windows.Handle
	path       string
	key        uintptr
	buffer     []byte // buffer must be aligned to a 64-bit boundary
	overlapped windows.Overlapped
	recursive  bool
}

// startPlatform initializes the Windows watcher backend
func (w *watcher) startPlatform(ctx context.Context) (<-chan struct{}, error) {
	iocp, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 0)
	if err != nil {
		return nil, newError("createIoCp", "", err)
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

	return done, nil
}

// runWindowsLoop is the main event processing loop for Windows
func (w *watcher) runWindowsLoop(iocp windows.Handle, done chan struct{}) {
	defer func() {
		w.lockPlatformShutdown()
		defer w.opsMu.Unlock()
		w.logDebug("Windows platform shutting down")
		w.streamMu.Lock()
		if pathMap, ok := w.streams["pathMap"].(map[uintptr]*watchedPath); ok {
			for key, wp := range pathMap {
				_ = windows.CancelIoEx(wp.handle, &wp.overlapped)
				_ = windows.Close(wp.handle)
				delete(pathMap, key)
				delete(w.streams, wp.path)
			}
		}
		w.streamMu.Unlock()
		_ = windows.Close(iocp)
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

		if err == windows.ERROR_OPERATION_ABORTED {
			continue
		}
		w.streamMu.Lock()
		pathMap := w.streams["pathMap"].(map[uintptr]*watchedPath)
		wp, exists := pathMap[key]
		if !exists {
			w.streamMu.Unlock()
			continue
		}
		completionErr := w.handleWindowsCompletion(wp, bytesRead, err, w.readChanges)
		w.streamMu.Unlock()
		if completionErr != nil {
			w.logError("Windows watch failed", "path", wp.path, "error", completionErr)
			return
		}

	}
}

// handleWindowsCompletion rearms recoverable overflows and retires deleted roots.
// Other failures stop the backend rather than leave a permanently silent root.
// The caller must hold streamMu.
func (w *watcher) handleWindowsCompletion(wp *watchedPath, n uint32, completionErr error, rearm func(*watchedPath) error) error {
	if completionErr == windows.ERROR_OPERATION_ABORTED {
		return nil
	}
	if completionErr != nil && completionErr != windows.ERROR_NOTIFY_ENUM_DIR && completionErr != windows.ERROR_MORE_DATA {
		return w.handleWindowsReadError(wp, "readCompletion", completionErr)
	}
	if completionErr != nil || n == 0 {
		w.handlePlatformEvent(WatchEvent{Path: wp.path, Types: []EventType{EventOverflow}, Time: time.Now()})
	} else {
		w.handleWindowsEvents(wp, n)
	}
	if err := rearm(wp); err != nil {
		return w.handleWindowsReadError(wp, "rearm", err)
	}
	return nil
}

// Windows can report a deleted directory as ERROR_ACCESS_DENIED both in an
// IOCP completion and when rearming its read. Confirm deletion so an actual
// permission failure on an existing directory is still returned to the caller.
func (w *watcher) handleWindowsReadError(wp *watchedPath, op string, err error) error {
	if !wp.wasRemoved(err) {
		return newError(op, wp.path, err)
	}
	if err := w.removeWindowsWatch(wp); err != nil {
		return newError("removeDeletedWatch", wp.path, err)
	}
	w.handlePlatformEvent(WatchEvent{Path: wp.path, Types: []EventType{EventRemove}, Time: time.Now()})
	return nil
}

func (wp *watchedPath) wasRemoved(err error) bool {
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_DELETE_PENDING) {
		return false
	}
	// FILE_STANDARD_INFO identifies the original handle even if a new directory
	// has already been created at the same path.
	var info struct {
		AllocationSize int64
		EndOfFile      int64
		NumberOfLinks  uint32
		DeletePending  byte
		Directory      byte
	}
	if windows.GetFileInformationByHandleEx(wp.handle, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))) == nil && info.DeletePending != 0 {
		return true
	}
	// Some filesystems do not support the handle query.
	_, statErr := os.Stat(wp.path)
	return errors.Is(statErr, os.ErrNotExist) || errors.Is(statErr, windows.ERROR_DELETE_PENDING)
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
		return newError("convertPath", path, err)
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
		return newError("createHandle", path, err)
	}

	// Allocate a uint64 slice to ensure 8-byte alignment, then slice it as bytes
	readSize := w.readBufferSize
	if volume := filepath.VolumeName(path); len(volume) >= 2 && volume[:2] == `\\` && readSize > 64*1024 {
		readSize = 64 * 1024
	}
	alignedBuf := make([]uint64, (readSize+7)/8)
	wp := &watchedPath{
		handle:    handle,
		path:      path,
		key:       key,
		buffer:    unsafe.Slice((*byte)(unsafe.Pointer(&alignedBuf[0])), len(alignedBuf)*8),
		recursive: watchPath.Depth != WatchTopLevel,
	}

	if _, err := windows.CreateIoCompletionPort(handle, iocp, key, 0); err != nil {
		windows.Close(handle)
		return newError("associateIoCp", path, err)
	}

	if err := w.readChanges(wp); err != nil {
		windows.Close(handle)
		return newError("initialRead", path, err)
	}

	w.streams[path] = wp
	pathMap[key] = wp
	w.logInfo("Added watch", "path", path)
	return nil
}

// removeWatch stops watching a directory on Windows
func (w *watcher) removeWatch(path string) error {
	w.streamMu.Lock()
	defer w.streamMu.Unlock()

	wpAny, exists := w.streams[path]
	if !exists {
		// Windows can complete and release a directory handle after the
		// directory is removed. DropPath still needs to clear its public entry.
		return nil
	}
	wp := wpAny.(*watchedPath)
	return w.removeWindowsWatch(wp)
}

// removeWindowsWatch releases one native registration. The configured root is
// retained until DropPath, including when Windows first reports its deletion.
// The caller must hold streamMu.
func (w *watcher) removeWindowsWatch(wp *watchedPath) error {
	path := wp.path
	pathMap := w.streams["pathMap"].(map[uintptr]*watchedPath)

	// Try to cancel any pending I/O operations
	if err := windows.CancelIoEx(wp.handle, &wp.overlapped); err != nil && err != windows.ERROR_NOT_FOUND {
		w.logWarn("Failed to cancel I/O", "path", path, "error", err)
		// If we can't cancel the I/O, the state is uncertain abort
		return err
	}

	// Close the handle
	closeErr := windows.Close(wp.handle)
	if closeErr != nil {
		w.logWarn("Failed to close handle", "path", path, "error", closeErr)
	}

	// Clean up internal state
	delete(pathMap, wp.key)
	delete(w.streams, path)
	w.logInfo("Removed watch", "path", path)

	return closeErr
}

// readChanges issues an asynchronous ReadDirectoryChangesW call
func (w *watcher) readChanges(wp *watchedPath) error {
	return readDirectoryChangesW(
		wp.handle,
		wp.buffer,
		wp.recursive,
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
