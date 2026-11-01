//go:build darwin

package fswatcher

import "golang.org/x/sys/unix"

// isPlatformHiddenComponent checks the macOS UF_HIDDEN flag for one path.
func isPlatformHiddenComponent(path string) bool {
	var stat unix.Stat_t
	return unix.Lstat(path, &stat) == nil && stat.Flags&unix.UF_HIDDEN != 0
}
