//go:build windows

package fswatcher

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWindowsOverflowRearmsRead(t *testing.T) {
	for _, completion := range []error{nil, windows.ERROR_NOTIFY_ENUM_DIR, windows.ERROR_MORE_DATA} {
		wi, err := New(WithPath(t.TempDir()), WithSeverity(SeverityNone))
		require.NoError(t, err)
		w := wi.(*watcher)
		calls := 0
		wp := &watchedPath{path: w.Paths()[0]}
		require.NoError(t, w.handleWindowsCompletion(wp, 0, completion, func(*watchedPath) error { calls++; return nil }))
		require.Equal(t, 1, calls)
		w.aggregator.close()
		require.Contains(t, (<-w.Events()).Types, EventOverflow)
	}
}

func TestWindowsReadFailuresAreReturned(t *testing.T) {
	wi, err := New(WithPath(t.TempDir()), WithSeverity(SeverityNone))
	require.NoError(t, err)
	impl := wi.(*watcher)
	defer impl.aggregator.close()
	wp := &watchedPath{path: impl.Paths()[0], handle: windows.InvalidHandle}
	for _, readErr := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_INVALID_HANDLE} {
		require.ErrorIs(t, impl.handleWindowsCompletion(wp, 0, readErr, func(*watchedPath) error { t.Fatal("must not rearm a failed handle"); return nil }), readErr)
		require.ErrorIs(t, impl.handleWindowsCompletion(wp, 0, nil, func(*watchedPath) error { return readErr }), readErr)
	}
	require.NoError(t, impl.handleWindowsCompletion(wp, 0, windows.ERROR_OPERATION_ABORTED, func(*watchedPath) error { t.Fatal("must not rearm cancellation"); return nil }))
}

func TestWindowsDeletedRootReadErrors(t *testing.T) {
	for _, rearming := range []bool{false, true} {
		for _, readErr := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_DELETE_PENDING} {
			stage := "completion/"
			if rearming {
				stage = "rearm/"
			}
			t.Run(stage+readErr.Error(), func(t *testing.T) {
				root := canonicalTempDir(t)
				wi, err := New(WithPath(root), WithSeverity(SeverityNone))
				require.NoError(t, err)
				w := wi.(*watcher)
				defer w.aggregator.close()
				path, err := windows.UTF16PtrFromString(root)
				require.NoError(t, err)
				handle, err := windows.CreateFile(path, windows.FILE_LIST_DIRECTORY,
					windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
					nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
				require.NoError(t, err)
				wp := &watchedPath{path: root, handle: handle, key: 1}
				pathMap := map[uintptr]*watchedPath{wp.key: wp}
				w.streams = map[string]any{root: wp, "pathMap": pathMap}
				defer func() {
					if _, exists := pathMap[wp.key]; exists {
						_ = windows.Close(handle)
					}
				}()
				require.NoError(t, os.Remove(root))
				completionErr := readErr
				if rearming {
					completionErr = nil
				}
				w.streamMu.Lock()
				err = w.handleWindowsCompletion(wp, 0, completionErr, func(*watchedPath) error {
					if !rearming {
						t.Error("deleted root must not be rearmed")
					}
					return readErr
				})
				w.streamMu.Unlock()
				require.NoError(t, err)
				require.Empty(t, pathMap)
				require.NotContains(t, w.streams, root)
				var info windows.ByHandleFileInformation
				require.ErrorIs(t, windows.GetFileInformationByHandle(handle, &info), windows.ERROR_INVALID_HANDLE)
				w.aggregator.close()
				foundRemoval := false
				for len(w.events) > 0 {
					event := <-w.events
					if event.Path == root {
						for _, kind := range event.Types {
							foundRemoval = foundRemoval || kind == EventRemove
						}
					}
				}
				require.True(t, foundRemoval, "deleted root must emit EventRemove")
			})
		}
	}
}
