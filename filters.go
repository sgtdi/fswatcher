package fswatcher

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// PathFilter defines the interface for path filtering logic
type PathFilter interface {
	ShouldInclude(path string) bool
}

// patternFilter implements PathFilter using include/exclude regex patterns
type patternFilter struct {
	includePatterns []*regexp.Regexp
	excludePatterns []*regexp.Regexp
	mu              sync.RWMutex
}

// newPatternFilter creates a new filter with the given regex patterns
func newPatternFilter(include, exclude []*regexp.Regexp) *patternFilter {
	return &patternFilter{
		includePatterns: include,
		excludePatterns: exclude,
	}
}

// ShouldInclude determines if a path should be processed, excluded patterns take precedence over included
func (f *patternFilter) ShouldInclude(path string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	base := filepath.Base(path)

	for _, re := range f.excludePatterns {
		if re.MatchString(path) || re.MatchString(base) {
			return false
		}
	}

	if len(f.includePatterns) == 0 {
		return true
	}

	for _, re := range f.includePatterns {
		if re.MatchString(path) || re.MatchString(base) {
			return true
		}
	}

	return false
}

// isSubpath reports whether the child path is inside the parent directory
func isSubpath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && !startsWithDotDot(rel)
}

// startsWithDotDot reports whether a relative path is the parent directory or
// starts with a parent-directory path segment.
func startsWithDotDot(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DefaultIgnores returns the default ignore list of system and temporary
// files on the current platform, as used by [WithIgnoreList]: the platform
// prefixes as "prefix*" patterns and suffixes as "*suffix" patterns
// The returned map is a new copy that can be modified, to add or delete
// patterns, and passed to [WithIgnoreList]
func DefaultIgnores() map[string]struct{} {
	ignores := make(map[string]struct{}, len(osPrefixes)+len(osSuffixes))
	for _, prefix := range osPrefixes {
		ignores[prefix+"*"] = struct{}{}
	}
	for _, suffix := range osSuffixes {
		ignores["*"+suffix] = struct{}{}
	}
	return ignores
}

// defaultIgnoreList is the sorted list of default ignore patterns
var defaultIgnoreList = sortedPatterns(DefaultIgnores())

// sortedPatterns returns the patterns of an ignore list, sorted
func sortedPatterns(ignores map[string]struct{}) []string {
	list := make([]string, 0, len(ignores))
	for pattern := range ignores {
		list = append(list, pattern)
	}
	sort.Strings(list)
	return list
}

// isIgnored checks whether a path is hidden or matches a temporary/system file
// pattern from [WithIgnoreList], or the defaults if no list was set.
func (w *watcher) isIgnored(path string) bool {
	return w.isIgnoredForWatch(path, w.findMostSpecificWatchPath(path))
}

// isIgnoredForWatch checks path filters when the matching watch is already
// known, avoiding a second scan of watched paths in the event pipeline.
func (w *watcher) isIgnoredForWatch(path string, watch *WatchPath) bool {
	if !w.includeHidden {
		root := ""
		if watch != nil {
			root = watch.Path
		}
		if isHiddenPathWithin(path, root) {
			return true
		}
	}

	return w.isIgnoredPattern(path)
}

// isIgnoredPattern reports whether a path matches the configured system and
// temporary file patterns, or the defaults if no list was set.
func (w *watcher) isIgnoredPattern(path string) bool {
	patterns := w.ignoreList
	if patterns == nil {
		patterns = defaultIgnoreList
	}
	base := filepath.Base(path)
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

// isHiddenPathWithin reports whether a path is hidden, checking ancestors only
// as far as the watched root when the path belongs to one.
func isHiddenPathWithin(path, root string) bool {
	return isPlatformHidden(path, root)
}

func hasHiddenPathComponentWithin(path, root string) bool {
	return walkPathAncestors(path, root, isDotHiddenComponent)
}

func isDotHiddenComponent(path string) bool {
	base := filepath.Base(path)
	return base != "." && base != ".." && strings.HasPrefix(base, ".")
}

// walkPathAncestors visits a path and its parents, stopping after root when it
// is an ancestor of path. An empty or unrelated root means the whole path.
func walkPathAncestors(path, root string, visit func(string) bool) bool {
	current := filepath.Clean(path)
	root = filepath.Clean(root)
	bounded := root != "." && (current == root || isSubpath(root, current))

	for {
		if visit(current) {
			return true
		}
		if bounded && current == root {
			return false
		}

		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}
