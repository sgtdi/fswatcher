//go:build windows

package fswatcher

import (
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
	"testing"
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
	w := &watcher{}
	wp := &watchedPath{path: "root"}
	require.ErrorIs(t, w.handleWindowsCompletion(wp, 0, windows.ERROR_ACCESS_DENIED, func(*watchedPath) error { t.Fatal("must not rearm a failed handle"); return nil }), windows.ERROR_ACCESS_DENIED)
	require.NoError(t, w.handleWindowsCompletion(wp, 0, windows.ERROR_OPERATION_ABORTED, func(*watchedPath) error { t.Fatal("must not rearm cancellation"); return nil }))
	wi, err := New(WithPath(t.TempDir()), WithSeverity(SeverityNone))
	require.NoError(t, err)
	impl := wi.(*watcher)
	defer impl.aggregator.close()
	require.ErrorIs(t, impl.handleWindowsCompletion(wp, 0, nil, func(*watchedPath) error { return windows.ERROR_INVALID_HANDLE }), windows.ERROR_INVALID_HANDLE)
}
