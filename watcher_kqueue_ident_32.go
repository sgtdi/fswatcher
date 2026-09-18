//go:build (freebsd || openbsd || netbsd) && (386 || arm)

package fswatcher

// kqueueIdent converts a file descriptor to the architecture-specific type
// used by unix.Kevent_t. On these BSD 32-bit targets, Kevent_t.Ident is uint32.
func kqueueIdent(fd int) uint32 {
	return uint32(fd)
}
