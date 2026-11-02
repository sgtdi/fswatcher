//go:build linux

package fswatcher

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func fanotifyNameRecord(fsid uint64, handle byte, name string) []byte {
	record := make([]byte, 21+len(name)+1)
	record[0] = unix.FAN_EVENT_INFO_TYPE_DFID_NAME
	binary.NativeEndian.PutUint16(record[2:4], uint16(len(record)))
	binary.NativeEndian.PutUint64(record[4:12], fsid)
	binary.NativeEndian.PutUint32(record[12:16], 1)
	binary.NativeEndian.PutUint32(record[16:20], 1)
	record[20] = handle
	copy(record[21:], name)
	return record
}

func TestFanotifyResolvesEachHandleOnSameFilesystem(t *testing.T) {
	calls := 0
	p := &fanotify{mountCache: map[uint64]string{7: "/old/cached-directory"}, resolveFn: func(fsid uint64, handle unix.FileHandle) (string, error) {
		require.Equal(t, uint64(7), fsid)
		calls++
		if handle.Bytes()[0] == 1 {
			return "/watch/a", nil
		}
		return "/watch/b", nil
	}}
	w := &watcher{}
	first, err := w.parseFanotifyInfo(p, fanotifyNameRecord(7, 1, "one.txt"))
	require.NoError(t, err)
	second, err := w.parseFanotifyInfo(p, fanotifyNameRecord(7, 2, "two.txt"))
	require.NoError(t, err)
	require.Equal(t, "/watch/a/one.txt", first)
	require.Equal(t, "/watch/b/two.txt", second)
	require.Equal(t, 2, calls)
	p.resolveFn = func(uint64, unix.FileHandle) (string, error) { return "", unix.ESTALE }
	_, err = w.parseFanotifyInfo(p, fanotifyNameRecord(7, 3, "deleted"))
	require.True(t, errors.Is(err, unix.ESTALE), "cache must not mask an invalid handle")
}

func TestFanotifyRejectsTruncatedInfo(t *testing.T) {
	record := fanotifyNameRecord(7, 1, "one.txt")
	for length := 1; length < len(record); length++ {
		_, err := (&watcher{}).parseFanotifyInfo(&fanotify{}, record[:length])
		require.Error(t, err)
	}
}
