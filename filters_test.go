package fswatcher

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatternFilter_ShouldInclude(t *testing.T) {
	testCases := []struct {
		name     string
		include  []string
		exclude  []string
		path     string
		expected bool
	}{
		{
			name:     "No filters",
			path:     "/any/path/file.txt",
			expected: true,
		},
		{
			name:     "Include match",
			include:  []string{`\.log$`},
			path:     "/tmp/include/app.log",
			expected: true,
		},
		{
			name:     "Include no match",
			include:  []string{`\.log$`},
			path:     "/any/path/app.txt",
			expected: false,
		},
		{
			name:     "Exclude match",
			exclude:  []string{`\.tmp$`},
			path:     "/any/path/session.tmp",
			expected: false,
		},
		{
			name:     "Exclude takes precedence",
			include:  []string{`\.go$`},
			exclude:  []string{`_test\.go$`},
			path:     "/project/src/main_test.go",
			expected: false,
		},
		{
			name:     "Include match with no exclude match",
			include:  []string{`\.go$`},
			exclude:  []string{`_test\.go$`},
			path:     "/project/src/main.go",
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var includeRegex, excludeRegex []*regexp.Regexp
			if tc.include != nil {
				for _, p := range tc.include {
					re, err := regexp.Compile(p)
					require.NoError(t, err)
					includeRegex = append(includeRegex, re)
				}
			}
			if tc.exclude != nil {
				for _, p := range tc.exclude {
					re, err := regexp.Compile(p)
					require.NoError(t, err)
					excludeRegex = append(excludeRegex, re)
				}
			}

			filter := newPatternFilter(includeRegex, excludeRegex)
			result := filter.ShouldInclude(tc.path)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsSubpathUsesPathSegmentBoundaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "watch")
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "same path", path: root},
		{name: "direct child", path: filepath.Join(root, "file.txt"), want: true},
		{name: "nested child", path: filepath.Join(root, "dir", "file.txt"), want: true},
		{name: "child beginning with two dots", path: filepath.Join(root, "..cache", "file.txt"), want: true},
		{name: "parent", path: filepath.Dir(root)},
		{name: "sibling with shared prefix", path: root + "-sibling"},
		{name: "child of sibling with shared prefix", path: filepath.Join(root+"-sibling", "file.txt")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isSubpath(root, tc.path))
		})
	}
}

func TestHiddenPathFiltering(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".hidden", "file.txt")
	pathIsHiddenByName := runtime.GOOS != "windows"

	t.Run("Default", func(t *testing.T) {
		w := watcherForHiddenPathTest(root)
		assert.Equal(t, pathIsHiddenByName, w.isIgnored(path))
		assert.Equal(t, pathIsHiddenByName, w.isIgnoredForWatch(path, &WatchPath{Path: root}))
		assert.False(t, w.isIgnored(filepath.Join(root, "visible.txt")))
	})

	t.Run("WithHidden", func(t *testing.T) {
		w := watcherForHiddenPathTest(root)
		WithHidden()(w)
		assert.False(t, w.isIgnored(path))
		assert.False(t, w.isIgnoredForWatch(path, &WatchPath{Path: root}))
	})

	t.Run("WithHiddenKeepsIgnorePatterns", func(t *testing.T) {
		w := watcherForHiddenPathTest(root)
		WithHidden()(w)
		path := filepath.Join(root, ".hidden", "file.tmp")
		assert.True(t, w.isIgnored(path))
		assert.True(t, w.isIgnoredForWatch(path, &WatchPath{Path: root}))
	})
}

func TestGitDirectoryUsesHiddenFilter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a .git name alone is not hidden on Windows")
	}

	root := t.TempDir()
	path := filepath.Join(root, ".git", "config")
	w := watcherForHiddenPathTest(root)
	assert.True(t, w.isIgnored(path), ".git should be excluded by the default dot-name rule")

	WithHidden()(w)
	assert.False(t, w.isIgnored(path), "WithHidden should include .git paths")
}

func TestIsIgnoredForWatchUsesProvidedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dot-prefixed paths are not the Windows hidden-file convention")
	}

	hiddenAncestor := filepath.Join(t.TempDir(), ".hidden-parent")
	root := filepath.Join(hiddenAncestor, "watched")
	w := &watcher{}

	assert.False(t, w.isIgnoredForWatch(filepath.Join(root, "visible.txt"), &WatchPath{Path: root}),
		"hidden ancestors above the provided watch root must not affect its children")
	assert.True(t, w.isIgnoredForWatch(filepath.Join(root, ".hidden", "secret.txt"), &WatchPath{Path: root}),
		"hidden components within the provided watch root must be filtered")
}

func TestHandlePlatformEventHiddenPaths(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	hiddenPath := filepath.Join(root, ".hidden", "secret.txt")
	visiblePath := filepath.Join(root, "visible.txt")
	pathIsHidden := runtime.GOOS != "windows"

	for _, tc := range []struct {
		name          string
		includeHidden bool
		wantHidden    bool
	}{
		{name: "default", wantHidden: !pathIsHidden},
		{name: "WithHidden", includeHidden: true, wantHidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan WatchEvent, 4)
			w := &watcher{
				watchedPaths: map[string]*WatchPath{
					root: {Path: root, Depth: WatchNested},
				},
				events:  events,
				dropped: make(chan WatchEvent, 4),
				filter:  newPatternFilter(nil, nil),
			}
			if tc.includeHidden {
				WithHidden()(w)
			}
			w.aggregator = newEventAggregator(w, time.Hour)

			w.handlePlatformEvent(WatchEvent{Path: hiddenPath, Types: []EventType{EventCreate}})
			w.handlePlatformEvent(WatchEvent{Path: visiblePath, Types: []EventType{EventCreate}})
			w.aggregator.close()

			got := make(map[string]bool, len(events))
			for len(events) > 0 {
				got[(<-events).Path] = true
			}
			want := map[string]bool{visiblePath: true}
			if tc.wantHidden {
				want[hiddenPath] = true
			}
			assert.Equal(t, want, got)
		})
	}
}

func watcherForHiddenPathTest(root string) *watcher {
	root = filepath.Clean(root)
	return &watcher{
		watchedPaths: map[string]*WatchPath{
			root: {Path: root},
		},
	}
}

func TestScanDirectoryHiddenPaths(t *testing.T) {
	root := t.TempDir()
	hiddenDir := filepath.Join(root, ".hidden")
	require.NoError(t, os.Mkdir(hiddenDir, 0o700))
	visibleFile := filepath.Join(root, "visible.txt")
	hiddenFile := filepath.Join(hiddenDir, "secret.txt")
	require.NoError(t, os.WriteFile(visibleFile, []byte("visible"), 0o600))
	require.NoError(t, os.WriteFile(hiddenFile, []byte("hidden"), 0o600))

	allPaths := map[string]bool{hiddenDir: true, hiddenFile: true, visibleFile: true}
	visiblePaths := map[string]bool{visibleFile: true}
	if runtime.GOOS == "windows" {
		visiblePaths = allPaths
	}

	tests := []struct {
		name          string
		includeHidden bool
		want          map[string]bool
	}{
		{name: "Default", want: visiblePaths},
		{name: "WithHidden", includeHidden: true, want: allPaths},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := watcherForHiddenPathTest(root)
			w.events = make(chan WatchEvent, 8)
			w.dropped = make(chan WatchEvent, 8)
			w.filter = newPatternFilter(nil, nil)
			w.aggregator = newEventAggregator(w, time.Millisecond)
			if tc.includeHidden {
				WithHidden()(w)
			}

			w.scanDirectory(root)
			w.aggregator.close()

			got := make(map[string]bool, len(w.events))
			for len(w.events) > 0 {
				got[(<-w.events).Path] = true
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestScanDirectoryUnderHiddenAncestor(t *testing.T) {
	root := t.TempDir()
	scanRoot := filepath.Join(root, ".hidden", "nested")
	child := filepath.Join(scanRoot, "visible-name.txt")
	require.NoError(t, os.MkdirAll(scanRoot, 0o700))
	require.NoError(t, os.WriteFile(child, []byte("content"), 0o600))

	for _, tc := range []struct {
		name          string
		includeHidden bool
	}{
		{name: "default"},
		{name: "WithHidden", includeHidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := watcherForHiddenPathTest(root)
			w.events = make(chan WatchEvent, 4)
			w.dropped = make(chan WatchEvent, 4)
			w.filter = newPatternFilter(nil, nil)
			w.aggregator = newEventAggregator(w, time.Hour)
			if tc.includeHidden {
				WithHidden()(w)
			}

			w.scanDirectory(scanRoot)
			w.aggregator.close()

			wantEvent := tc.includeHidden || runtime.GOOS == "windows"
			if wantEvent {
				require.Len(t, w.events, 1)
				assert.Equal(t, child, (<-w.events).Path)
			} else {
				assert.Empty(t, w.events)
			}
		})
	}
}

func TestIgnoreList(t *testing.T) {
	defaults := DefaultIgnores()
	require.NotEmpty(t, defaults)
	assert.Contains(t, defaults, "*.tmp", "*.tmp is a default on all platforms")

	t.Run("Defaults", func(t *testing.T) {
		w, err := New(WithPath(tempDir))
		require.NoError(t, err)
		ww := w.(*watcher)
		assert.True(t, ww.isIgnored("/path/file.tmp"))
		assert.False(t, ww.isIgnored("/path/file.go"))
	})

	t.Run("Customized", func(t *testing.T) {
		ignores := DefaultIgnores()
		delete(ignores, "*.tmp")
		ignores["*.orig"] = struct{}{}
		ignores["backup-*"] = struct{}{}
		w, err := New(WithPath(tempDir), WithIgnoreList(ignores))
		require.NoError(t, err)
		ww := w.(*watcher)
		assert.False(t, ww.isIgnored("/path/file.tmp"), "deleted default")
		assert.True(t, ww.isIgnored("/path/file.orig"), "added pattern")
		assert.True(t, ww.isIgnored("/path/backup-1.go"), "added pattern")
		assert.False(t, ww.isIgnored("/path/backup/file.go"), "only the base name is matched")
		assert.True(t, ww.isIgnored("/path/file.bak"), "other defaults kept")
		assert.Contains(t, DefaultIgnores(), "*.tmp", "defaults not modified")
	})

	t.Run("Empty", func(t *testing.T) {
		w, err := New(WithPath(tempDir), WithIgnoreList(map[string]struct{}{}))
		require.NoError(t, err)
		ww := w.(*watcher)
		assert.False(t, ww.isIgnored("/path/file.tmp"))
		assert.False(t, ww.isIgnored("/path/~file"))
	})

	t.Run("InvalidPattern", func(t *testing.T) {
		w, err := New(WithPath(tempDir), WithIgnoreList(map[string]struct{}{"[": {}}))
		assert.Nil(t, w)
		assert.Error(t, err)
	})
}
