//go:build darwin

package fswatcher

import "golang.org/x/sys/unix"

// isPlatformHidden checks the macOS UF_HIDDEN flag on the path and its parent
// directories. Dot-prefixed path components are checked in filters.go.
func isPlatformHidden(path, root string) bool {
	return walkPathAncestors(path, root, func(current string) bool {
		if isDotHiddenComponent(current) {
			return true
		}
		var stat unix.Stat_t
		if err := unix.Lstat(current, &stat); err == nil && stat.Flags&unix.UF_HIDDEN != 0 {
			return true
		}
		return false
	})
}
