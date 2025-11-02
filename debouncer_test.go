package fswatcher

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func makeEvent(path string, t time.Time) WatchEvent {
	return WatchEvent{
		ID:    0,
		Path:  path,
		Types: []EventType{EventMod},
		Flags: []string{},
		Time:  t,
	}
}

func TestCooldownDebouncer(t *testing.T) {
	cooldown := 50 * time.Millisecond
	debouncer := newDebouncer(cooldown)

	path1 := "/tmp/file1.txt"
	path2 := "/tmp/file2.txt"
	now := time.Now()

	// First event for a path should always be processed.
	ev1 := makeEvent(path1, now)
	_, ok := debouncer.ShouldProcess(ev1)
	assert.True(t, ok, "First event should be processed")

	// Second event for the same path within cooldown should be merged/ignored.
	ev2 := makeEvent(path1, now.Add(cooldown/2))
	_, ok = debouncer.ShouldProcess(ev2)
	assert.False(t, ok, "Second event within cooldown should be ignored/merged")

	// Event for a different path should be processed.
	ev3 := makeEvent(path2, now.Add(cooldown/2))
	_, ok = debouncer.ShouldProcess(ev3)
	assert.True(t, ok, "Event for a different path should be processed")

	// Event for the first path after cooldown should be processed.
	time.Sleep(cooldown + 50*time.Millisecond)
	ev4 := makeEvent(path1, time.Now())
	_, ok = debouncer.ShouldProcess(ev4)
	assert.True(t, ok, "Event after cooldown should be processed")

	// Test with zero cooldown (should always process).
	zeroDebouncer := newDebouncer(0)
	_, ok = zeroDebouncer.ShouldProcess(ev1)
	assert.True(t, ok, "Event with zero cooldown should be processed")
	_, ok = zeroDebouncer.ShouldProcess(ev1)
	assert.True(t, ok, "Second event with zero cooldown should also be processed")
}

func TestDebouncerCleanup(t *testing.T) {
	debouncer := newDebouncer(1 * time.Minute)
	maxAge := 10 * time.Millisecond

	// Add a bunch of entries to exceed the cleanup threshold
	for i := 0; i < CleanupThreshold+10; i++ {
		ev := makeEvent(fmt.Sprintf("/tmp/file%d", i), time.Now())
		debouncer.ShouldProcess(ev)
	}
	assert.Equal(t, CleanupThreshold+10, len(debouncer.lastSeen), "Should have all entries before cleanup")

	// Wait for entries to become stale
	time.Sleep(maxAge * 2)

	// Add one new entry that is not stale
	newEv := makeEvent("/tmp/newfile", time.Now())
	debouncer.ShouldProcess(newEv)

	cleanedCount := debouncer.Cleanup(maxAge)
	assert.Equal(t, CleanupThreshold+10, cleanedCount, "Should have cleaned the stale entries")
	assert.Equal(t, 1, len(debouncer.lastSeen), "Only the new entry should remain")
	_, exists := debouncer.lastSeen["/tmp/newfile"]
	assert.True(t, exists, "The non-stale entry should exist")
}

func TestDebouncerReset(t *testing.T) {
	debouncer := newDebouncer(1 * time.Minute)
	ev1 := makeEvent("/tmp/file1", time.Now())
	ev2 := makeEvent("/tmp/file2", time.Now())
	debouncer.ShouldProcess(ev1)
	debouncer.ShouldProcess(ev2)
	assert.Equal(t, 2, len(debouncer.lastSeen))

	debouncer.Reset()
	assert.Equal(t, 0, len(debouncer.lastSeen), "Cache should be empty after reset")
}
