//go:build windows

package fswatcher

import "golang.org/x/sys/windows"

// isPlatformHiddenComponent checks Windows' hidden attribute for one path.
func isPlatformHiddenComponent(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(name)
	return err == nil && attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0
}
