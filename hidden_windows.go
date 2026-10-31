//go:build windows

package fswatcher

import "golang.org/x/sys/windows"

// isPlatformHidden checks the Windows hidden attribute on the path and its
// parent directories. A dot-prefixed name alone is not hidden on Windows.
func isPlatformHidden(path, root string) bool {
	return walkPathAncestors(path, root, func(current string) bool {
		if name, err := windows.UTF16PtrFromString(current); err == nil {
			if attrs, err := windows.GetFileAttributes(name); err == nil && attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
				return true
			}
		}
		return false
	})
}
