//go:build !windows && !darwin && !freebsd

package fswatcher

// Linux, OpenBSD, NetBSD, and DragonFly use dot-prefixed names as their
// hidden-file convention and have no separate hidden attribute.
func isPlatformHiddenComponent(string) bool { return false }
