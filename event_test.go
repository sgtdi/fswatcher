package fswatcher

import (
	"math/rand"
	"testing"
	"time"
)

func TestIsSystemFile(t *testing.T) {
	testCases := []struct {
		name     string
		path     string
		expected bool
	}{
		{"Normal file", "/path/document.docx", false},
	}

	if len(osPrefixes) > 0 {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		randomIndex := r.Intn(len(osPrefixes))
		randomPrefix := osPrefixes[randomIndex]
		testCases = append(testCases, struct {
			name     string
			path     string
			expected bool
		}{"Dynamic system file prefix", "/path/" + randomPrefix + "file", true})
	}

	if len(osSuffixes) > 0 {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		randomIndex := r.Intn(len(osSuffixes))
		randomSuffix := osSuffixes[randomIndex]
		testCases = append(testCases, struct {
			name     string
			path     string
			expected bool
		}{"Dynamic system file suffix", "/path/file" + randomSuffix, true})
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := isSystemFile(tc.path)
			if actual != tc.expected {
				t.Errorf("isSystemFile(%q) = %v; want %v", tc.path, actual, tc.expected)
			}
		})
	}
}
