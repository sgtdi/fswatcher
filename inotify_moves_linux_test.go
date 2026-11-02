//go:build linux

package fswatcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestInotifyDirectoryMovesKeepPathsConsistent(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	nested := filepath.Join(old, "nested")
	require.NoError(t, os.MkdirAll(nested, 0700))
	file := filepath.Join(nested, "existing.dat")
	require.NoError(t, os.WriteFile(file, []byte("old"), 0600))
	ready := make(chan struct{})
	wi, err := New(WithPath(root), WithReadyChannel(ready), WithCooldown(time.Millisecond), WithSeverity(SeverityNone))
	require.NoError(t, err)
	w := wi.(*watcher)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- w.Watch(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped; require.NoError(t, <-result) }()
	select {
	case <-ready:
	case <-stopped:
		t.Fatal("startup failed")
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	p := w.streams["platform"].(*inotify)
	renamed := filepath.Join(root, "renamed")
	require.NoError(t, os.Rename(old, renamed))
	require.Eventually(t, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		node := p.trie.findNode(filepath.Join(renamed, "nested"))
		return node != nil && node.wd >= 0 && len(p.moves) == 0
	}, time.Second, time.Millisecond)
	newFile := filepath.Join(renamed, "nested", "existing.dat")
	require.NoError(t, os.WriteFile(newFile, []byte("new"), 0600))
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
received:
	for {
		select {
		case event := <-w.Events():
			if event.Path == newFile {
				break received
			}
		case <-timeout.C:
			t.Fatal("no event at the renamed path")
		}
	}
	outside := filepath.Join(t.TempDir(), "moved-out")
	require.NoError(t, os.Rename(renamed, outside))
	require.Eventually(t, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		for _, wp := range p.wds {
			if wp.Path == renamed || isSubpath(renamed, wp.Path) {
				return false
			}
		}
		return len(p.moves) == 0
	}, time.Second, time.Millisecond)
	// Recreating the same name after move-out must get fresh watches.
	require.NoError(t, os.MkdirAll(filepath.Join(renamed, "fresh"), 0700))
	require.Eventually(t, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		n := p.trie.findNode(filepath.Join(renamed, "fresh"))
		return n != nil && n.wd >= 0
	}, time.Second, time.Millisecond)
}

func TestInotifyMoveExpiryDoesNotRemoveReplacement(t *testing.T) {
	w := &watcher{}
	p := &inotify{fd: -1, wds: map[int]*WatchPath{1: {Path: "/watch/old"}, 2: {Path: "/watch/old/child"}}, trie: newPathTrieNode(), w: w}
	p.beginMove(7, "/watch/old", time.Now())
	p.wds[3] = &WatchPath{Path: "/watch/old"}
	p.expireMoves(time.Now().Add(inotifyMoveGrace))
	require.Equal(t, map[int]*WatchPath{3: {Path: "/watch/old"}}, p.wds)
}

func TestInotifyMoveSelfWithoutParentDropsSubtree(t *testing.T) {
	w := &watcher{}
	p := &inotify{fd: -1, wds: map[int]*WatchPath{1: {Path: "/watch"}, 2: {Path: "/watch/child"}}, trie: newPathTrieNode()}
	p.rebuildTrieLocked()
	require.False(t, w.handleWatchRemovalEvents(p, &unix.InotifyEvent{Wd: 1, Mask: unix.IN_MOVE_SELF}, "/watch"))
	require.Empty(t, p.wds)
}

func TestInotifyOverflowRebuildsDirectoryCoverage(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	require.NoError(t, os.Mkdir(old, 0700))
	wi, err := New(WithPath(root), WithSeverity(SeverityNone))
	require.NoError(t, err)
	w := wi.(*watcher)
	p, err := newInotify()
	require.NoError(t, err)
	defer unix.Close(p.fd)
	defer w.aggregator.close()
	require.NoError(t, p.addWatch(w, &WatchPath{Path: root, Depth: WatchNested}))
	oldWD := p.trie.findNode(old).wd
	require.NoError(t, os.Rename(old, filepath.Join(t.TempDir(), "outside")))
	fresh := filepath.Join(root, "old", "fresh")
	require.NoError(t, os.MkdirAll(fresh, 0700))
	w.handleInotifyEvent(p, &unix.InotifyEvent{Mask: unix.IN_Q_OVERFLOW}, time.Now(), nil, 0)
	require.NotContains(t, p.wds, oldWD)
	require.NotNil(t, p.trie.findNode(fresh))
	w.aggregator.close()
	require.Contains(t, (<-w.Events()).Types, EventOverflow)
}
