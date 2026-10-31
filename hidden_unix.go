//go:build !windows && !darwin && !freebsd

package fswatcher

// Linux, OpenBSD, NetBSD, and DragonFly use dot-prefixed names as their
// hidden-file convention.
func isPlatformHidden(path, root string) bool {
	return hasHiddenPathComponentWithin(path, root)
}
