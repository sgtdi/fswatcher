//go:build freebsd

package fswatcher

import "golang.org/x/sys/unix"

const freeBSDUFHidden = 0x00008000

// isPlatformHiddenComponent checks FreeBSD's UF_HIDDEN file flag.
func isPlatformHiddenComponent(path string) bool {
	var stat unix.Stat_t
	return unix.Lstat(path, &stat) == nil && stat.Flags&freeBSDUFHidden != 0
}
