package fswatcher

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EventType represents a filesystem event type
type EventType uint32

// Cross-platform event types
const (
	EventUnknown EventType = iota
	EventCreate
	EventRemove
	EventMod
	EventRename
	EventChmod
)

// eventTypeNames maps EventType to its string representation
var eventTypeNames = [...]string{"Unknown", "Create", "Delete", "Edit", "Rename", "Chmod"}

// String returns the string representation of an EventType
func (e EventType) String() string {
	if e < EventUnknown || e > EventChmod {
		return "Invalid"
	}
	return eventTypeNames[e]
}

// WatchEvent represents a single filesystem event
type WatchEvent struct {
	ID    uint64      // Platform-specific event ID
	Path  string      // Path of the file or directory
	Flags []string    // Platform-specific event flags
	Types []EventType // Cross-platform event types (Create, Remove, etc)
	Time  time.Time   // Time the event was received
}

// String returns a formatted string representation of a WatchEvent
func (we WatchEvent) String() string {
	var typeStrings []string
	for _, t := range we.Types {
		typeStrings = append(typeStrings, t.String())
	}
	return fmt.Sprintf("Event{ID: %d, Path: %q, Types: [%s], Flags: [%s], Time: %s}",
		we.ID, we.Path, strings.Join(typeStrings, ", "), strings.Join(we.Flags, ", "), we.Time.Format(time.RFC3339Nano))
}

// eventBatcher merges multiple events for the same path over a short duration
type eventBatcher struct {
	events      map[string]*batchedEvent
	timer       *time.Timer
	batchPeriod time.Duration
	watcher     *watcher
	mu          sync.Mutex
	isClosed    atomic.Bool
}

// batchedEvent holds merged data for events on the same path
type batchedEvent struct {
	path      string
	types     map[EventType]struct{}
	flags     map[string]struct{} // Use a map for automatic flag de-duplication
	firstSeen time.Time
	lastSeen  time.Time
	eventID   uint64
}

// newEventBatcher creates a new event batcher
func newEventBatcher(w *watcher, batchPeriod time.Duration) *eventBatcher {
	return &eventBatcher{
		events:      make(map[string]*batchedEvent),
		batchPeriod: batchPeriod,
		watcher:     w,
	}
}

// addEvent adds an event to the current batch
func (eb *eventBatcher) addEvent(event WatchEvent) {
	if eb.isClosed.Load() {
		return
	}

	eb.mu.Lock()
	defer eb.mu.Unlock()

	now := time.Now()

	if existing, exists := eb.events[event.Path]; exists {
		// Update existing batched event
		existing.lastSeen = now
		existing.eventID = event.ID // Keep the latest event ID

		for _, t := range event.Types {
			existing.types[t] = struct{}{}
		}
		for _, f := range event.Flags {
			existing.flags[f] = struct{}{}
		}
	} else {
		// Create a new batched event
		types := make(map[EventType]struct{}, len(event.Types))
		for _, t := range event.Types {
			types[t] = struct{}{}
		}
		flags := make(map[string]struct{}, len(event.Flags))
		for _, f := range event.Flags {
			flags[f] = struct{}{}
		}

		eb.events[event.Path] = &batchedEvent{
			path:      event.Path,
			types:     types,
			flags:     flags,
			firstSeen: now,
			lastSeen:  now,
			eventID:   event.ID,
		}
	}

	// Reset or start the timer to flush the batch
	if eb.timer != nil {
		eb.timer.Stop()
	}
	eb.timer = time.AfterFunc(eb.batchPeriod, eb.flush)
}

// flush sends all batched events
func (eb *eventBatcher) flush() {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	// Don't flush if the batcher was already closed and flushed
	if eb.isClosed.Load() {
		return
	}

	eb.flushLocked()
}

// flushLocked performs the flush operation while holding the lock
func (eb *eventBatcher) flushLocked() {
	if len(eb.events) == 0 {
		return
	}

	for path, batched := range eb.events {

		// Collect unique types and flags
		types := make([]EventType, 0, len(batched.types))
		for t := range batched.types {
			types = append(types, t)
		}

		flags := make([]string, 0, len(batched.flags))
		for f := range batched.flags {
			flags = append(flags, f)
		}

		finalEvent := WatchEvent{
			ID:    batched.eventID,
			Path:  path,
			Types: types,
			Flags: flags,
			Time:  batched.lastSeen,
		}

		eb.watcher.sendToChannel(finalEvent)
	}

	// Clear the batch for the next set of events
	eb.events = make(map[string]*batchedEvent)
}

// close stops the batcher and flushes any remaining events
func (eb *eventBatcher) close() {
	// Ensure close logic runs only once
	if !eb.isClosed.CompareAndSwap(false, true) {
		return
	}

	eb.mu.Lock()
	defer eb.mu.Unlock()

	// Stop the timer to prevent it from firing again
	if eb.timer != nil {
		eb.timer.Stop()
	}

	// Final flush before exiting
	eb.flushLocked()
}

// uniqueEventTypes removes duplicate EventType values from a slice
func uniqueEventTypes(s []EventType) []EventType {
	if len(s) < 2 {
		return s
	}

	// The max value is EventChmod (5)
	const maxEventType = EventChmod + 1
	var seen [maxEventType]bool

	j := 0
	for _, v := range s {
		// Ignore any invalid event types
		if v < maxEventType && !seen[v] {
			seen[v] = true
			s[j] = v
			j++
		}
	}
	return s[:j]
}
