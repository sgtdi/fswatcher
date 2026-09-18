//go:build (darwin && !cgo) || dragonfly || ((freebsd || openbsd || netbsd) && !(386 || arm))

package fswatcher

// kqueueIdent converts a file descriptor to the architecture-specific type
// used by unix.Kevent_t. On these kqueue targets, Kevent_t.Ident is uint64.
func kqueueIdent(fd int) uint64 {
	return uint64(fd)
}
