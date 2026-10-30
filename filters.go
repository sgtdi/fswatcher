package fswatcher

import (
	"path/filepath"
	"regexp"
	"sort"
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
	return err == nil && rel != "." && rel != ".." && !startsWithDotDot(rel)
}

// startsWithDotDot checks if a relative path string starts with "../"
func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
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

// isIgnored checks if a path is likely a temporary or system-generated file,
// using the ignore list set by [WithIgnoreList], or the defaults if not set
func (w *watcher) isIgnored(path string) bool {
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
