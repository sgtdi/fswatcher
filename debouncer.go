package fswatcher

import (
	"sync"
	"time"
)

// debouncer handles event debouncing logic
type debouncer struct {
	lastSeen map[string]WatchEvent
	cooldown time.Duration
	mu       sync.RWMutex
}

// Debouncer defines the interface for event debouncing
type Debouncer interface {
	ShouldProcess(ev WatchEvent) (WatchEvent, bool)
	Cleanup(maxAge time.Duration) int
	Reset()
}

// newDebouncer creates a new debouncer with a given cooldown
func newDebouncer(cooldown time.Duration) *debouncer {
	return &debouncer{
		lastSeen: make(map[string]WatchEvent),
		cooldown: cooldown,
	}
}

// mergeEvents combines two events for the same path into a single event
func mergeEvents(last, ev WatchEvent) WatchEvent {
	merged := last
	merged.Types = uniqueEventTypes(append(merged.Types, ev.Types...))
	merged.Flags = append(merged.Flags, ev.Flags...)
	if ev.ID > merged.ID {
		merged.ID = ev.ID
	}
	// Ensure the timestamp of the merged event is the most recent one
	if ev.Time.After(merged.Time) {
		merged.Time = ev.Time
	}
	return merged
}

// ShouldProcess checks if an event should be processed or debounced
func (d *debouncer) ShouldProcess(ev WatchEvent) (WatchEvent, bool) {
	if d.cooldown <= 0 {
		return ev, true
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	last, exists := d.lastSeen[ev.Path]
	if exists {
		// If events have the same timestamp or are within the cooldown merge them
		if ev.Time.Equal(last.Time) || ev.Time.Sub(last.Time) < d.cooldown {
			merged := mergeEvents(last, ev)
			d.lastSeen[ev.Path] = merged
			return merged, false
		}
	}

	d.lastSeen[ev.Path] = ev
	return ev, true
}

// Reset clears the debouncer's history of seen paths
func (d *debouncer) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastSeen = make(map[string]WatchEvent)
}

// Cleanup removes entries older than the specified max age
func (d *debouncer) Cleanup(maxAge time.Duration) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.lastSeen) < CleanupThreshold {
		return 0
	}

	now := time.Now()
	toDelete := make([]string, 0, len(d.lastSeen)/2)

	for path, lastEv := range d.lastSeen {
		if now.Sub(lastEv.Time) > maxAge {
			toDelete = append(toDelete, path)
		}
	}

	for _, path := range toDelete {
		delete(d.lastSeen, path)
	}

	return len(toDelete)
}
