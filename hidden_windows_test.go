//go:build windows

package fswatcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWindowsHiddenAttributeFiltering(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "hidden.txt")
	require.NoError(t, os.WriteFile(file, []byte("test"), 0o600))
	setWindowsHidden(t, file)

	assert.True(t, watcherForHiddenPathTest(root).isIgnored(file))
	assert.True(t, watcherForHiddenPathTest(root).isIgnoredForWatch(file, &WatchPath{Path: root}))

	w := watcherForHiddenPathTest(root)
	WithHidden()(w)
	assert.False(t, w.isIgnored(file))
	assert.False(t, w.isIgnoredForWatch(file, &WatchPath{Path: root}))

	dir := filepath.Join(root, "hidden-dir")
	require.NoError(t, os.Mkdir(dir, 0o700))
	child := filepath.Join(dir, "child.txt")
	require.NoError(t, os.WriteFile(child, []byte("test"), 0o600))
	setWindowsHidden(t, dir)
	assert.True(t, watcherForHiddenPathTest(root).isIgnored(child), "a hidden parent directory hides its descendants")
	assert.True(t, watcherForHiddenPathTest(root).isIgnoredForWatch(child, &WatchPath{Path: root}), "a hidden parent directory hides its descendants")
}

func setWindowsHidden(t *testing.T, path string) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	attrs, err := windows.GetFileAttributes(name)
	require.NoError(t, err)
	require.NoError(t, windows.SetFileAttributes(name, attrs|windows.FILE_ATTRIBUTE_HIDDEN))
	t.Cleanup(func() {
		attrs, err := windows.GetFileAttributes(name)
		if err == nil {
			_ = windows.SetFileAttributes(name, attrs&^windows.FILE_ATTRIBUTE_HIDDEN)
		}
	})
}
