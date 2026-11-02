package fswatcher

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReadBufferMustFitNativeEvents(t *testing.T) {
	for _, size := range []int{1, 255, MinReadBufferSize - 1} {
		_, err := New(WithPath(t.TempDir()), WithReadBufferSize(size))
		require.Error(t, err)
	}
	_, err := New(WithPath(t.TempDir()), WithReadBufferSize(MinReadBufferSize))
	require.NoError(t, err)
}
