//go:build darwin && cgo

package fswatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFSEventsMetadataRefreshesHiddenCache(t *testing.T) {
	for _, flag := range []uint32{FSEventStreamEventFlagItemInodeMetaMod, FSEventStreamEventFlagItemFinderInfoMod, FSEventStreamEventFlagItemXattrMod} {
		t.Run(fmt.Sprintf("flags_%x", flag), func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			path := filepath.Join(root, "visible.dat")
			require.NoError(t, os.WriteFile(path, []byte("data"), 0600))
			wi, err := New(WithPath(root), WithSeverity(SeverityNone))
			require.NoError(t, err)
			w := wi.(*watcher)
			defer w.aggregator.close()
			require.False(t, w.isIgnored(path))
			require.NoError(t, unix.Chflags(path, unix.UF_HIDDEN))
			defer unix.Chflags(path, 0)
			w.handleDarwinEvent(path, flag|FSEventStreamEventFlagItemIsFile, 1, time.Now())
			require.True(t, w.isIgnored(path))
			require.Empty(t, w.aggregator.events, "hidden metadata event must not be delivered")
			require.NoError(t, unix.Chflags(path, 0))
			w.handleDarwinEvent(path, flag|FSEventStreamEventFlagItemIsFile, 2, time.Now())
			require.False(t, w.isIgnored(path))
			w.aggregator.close()
			require.Equal(t, path, (<-w.Events()).Path)
		})
	}
}

func TestFSEventsDroppedFlagsEmitOverflow(t *testing.T) {
	for _, flag := range []uint32{FSEventStreamEventFlagMustScanSubDirs, FSEventStreamEventFlagUserDropped, FSEventStreamEventFlagKernelDropped} {
		wi, err := New(WithPath(t.TempDir(), WithEventMask(EventRemove)), WithSeverity(SeverityNone))
		require.NoError(t, err)
		w := wi.(*watcher)
		root := w.Paths()[0]
		w.handleDarwinEvent(root, flag, 1, time.Now())
		w.scanWorkers.Wait()
		w.aggregator.close()
		require.Len(t, w.events, 1)
		require.Contains(t, (<-w.Events()).Types, EventOverflow)
	}
}
