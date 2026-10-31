//go:build freebsd

package fswatcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFreeBSDHiddenFlagFiltering(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "hidden.txt")
	require.NoError(t, os.WriteFile(file, []byte("test"), 0o600))
	if err := unix.Chflags(file, freeBSDUFHidden); err != nil {
		t.Skipf("filesystem does not support UF_HIDDEN: %v", err)
	}
	t.Cleanup(func() { _ = unix.Chflags(file, 0) })

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
	if err := unix.Chflags(dir, freeBSDUFHidden); err != nil {
		t.Skipf("filesystem does not support UF_HIDDEN on directories: %v", err)
	}
	t.Cleanup(func() { _ = unix.Chflags(dir, 0) })
	assert.True(t, watcherForHiddenPathTest(root).isIgnored(child), "a hidden parent directory hides its descendants")
	assert.True(t, watcherForHiddenPathTest(root).isIgnoredForWatch(child, &WatchPath{Path: root}), "a hidden parent directory hides its descendants")
}
