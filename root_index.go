package fswatcher

import (
	"path/filepath"
	"runtime"
	"strings"
)

// watchRootNode indexes roots by path components, preserving segment boundaries
// and the most-specific-root precedence used by per-path filters.
type watchRootNode struct {
	children map[string]*watchRootNode
	watch    *WatchPath
}

func pathComponent(path string, start int) (string, int) {
	for start < len(path) && osPathSeparator(path[start]) {
		start++
	}
	end := start
	for end < len(path) && !osPathSeparator(path[end]) {
		end++
	}
	component := path[start:end]
	if runtime.GOOS == "windows" {
		component = strings.ToLower(component)
	}
	return component, end
}

func osPathSeparator(c byte) bool {
	return c == byte(filepath.Separator) || (runtime.GOOS == "windows" && c == '/')
}

func (w *watcher) rebuildRootIndexLocked() {
	index := &watchRootNode{}
	for path, watch := range w.watchedPaths {
		node := index
		for offset := 0; offset < len(path); {
			component, next := pathComponent(path, offset)
			offset = next
			if component == "" {
				break
			}
			if node.children == nil {
				node.children = make(map[string]*watchRootNode)
			}
			if node.children[component] == nil {
				node.children[component] = &watchRootNode{}
			}
			node = node.children[component]
		}
		node.watch = watch
	}
	w.rootIndex = index
}

func (w *watcher) ensureRootIndex() {
	w.pathMu.RLock()
	ready := w.rootIndex != nil
	w.pathMu.RUnlock()
	if ready {
		return
	}
	w.pathMu.Lock()
	if w.rootIndex == nil {
		w.rebuildRootIndexLocked()
	}
	w.pathMu.Unlock()
}

func (n *watchRootNode) find(path string) *WatchPath {
	if !filepath.IsAbs(path) {
		return nil
	}
	path = filepath.Clean(path)
	best := n.watch
	for offset := 0; offset < len(path); {
		component, next := pathComponent(path, offset)
		offset = next
		if component == "" {
			break
		}
		n = n.children[component]
		if n == nil {
			break
		}
		if n.watch != nil {
			best = n.watch
		}
	}
	return best
}
