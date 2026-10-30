//go:build freebsd

package fswatcher

import "golang.org/x/sys/unix"

const freeBSDUFHidden = 0x00008000

// isPlatformHidden checks FreeBSD's UF_HIDDEN file flag on the path and its
// parent directories. Dot-prefixed path components are checked in filters.go.
func isPlatformHidden(path, root string) bool {
	return walkPathAncestors(path, root, func(current string) bool {
		if isDotHiddenComponent(current) {
			return true
		}
		var stat unix.Stat_t
		if err := unix.Lstat(current, &stat); err == nil && stat.Flags&freeBSDUFHidden != 0 {
			return true
		}
		return false
	})
}
