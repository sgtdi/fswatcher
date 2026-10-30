package fswatcher

import (
	"regexp"
	"testing"

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
