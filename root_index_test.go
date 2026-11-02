package fswatcher

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestRootIndexSelectsMostSpecificPath(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	parentWatch, childWatch := &WatchPath{Path: root}, &WatchPath{Path: child, Depth: WatchTopLevel}
	w := &watcher{watchedPaths: map[string]*WatchPath{root: parentWatch, child: childWatch}}
	require.Same(t, childWatch, w.findMostSpecificWatchPath(filepath.Join(child, "file")))
	require.Same(t, childWatch, w.findMostSpecificWatchPath(child))
	require.Same(t, parentWatch, w.findMostSpecificWatchPath(filepath.Join(root, "child-sibling", "file")))
	require.Nil(t, w.findMostSpecificWatchPath(root+"-sibling"))
	require.Nil(t, w.findMostSpecificWatchPath("relative"))
}

func TestRootIndexHandlesFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	wp := &WatchPath{Path: root}
	w := &watcher{watchedPaths: map[string]*WatchPath{root: wp}}
	require.Same(t, wp, w.findMostSpecificWatchPath(filepath.Join(root, "folder", "file")))
}

func BenchmarkRootLookup(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("roots=%d", count), func(b *testing.B) {
			base := b.TempDir()
			w := &watcher{watchedPaths: make(map[string]*WatchPath)}
			var path string
			for i := 0; i < count; i++ {
				root := filepath.Join(base, fmt.Sprintf("%06d", i))
				w.watchedPaths[root] = &WatchPath{Path: root}
				path = filepath.Join(root, "payload.dat")
			}
			w.ensureRootIndex()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w.findMostSpecificWatchPath(path)
			}
		})
	}
}
