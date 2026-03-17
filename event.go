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
	EventOverflow
)

// eventTypeNames maps EventType to its string representation
var eventTypeNames = [...]string{"Unknown", "Create", "Delete", "Edit", "Rename", "Chmod", "Overflow"}

// String returns the string representation of an EventType
func (e EventType) String() string {
	if e < EventUnknown || e > EventOverflow {
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

// EventAggregator merges multiple events for the same path over a short duration
type EventAggregator struct {
	events   map[string]*aggregatedEvent
	cooldown time.Duration
	watcher  *watcher
	mu       sync.Mutex
	isClosed atomic.Bool
}

// aggregatedEvent holds merged data for events on the same path
type aggregatedEvent struct {
	path     string
	types    map[EventType]struct{}
	flags    map[string]struct{}
	lastSeen time.Time
	eventID  uint64
	timer    *time.Timer
}

// newEventAggregator creates a new event aggregator
func newEventAggregator(w *watcher, cooldown time.Duration) *EventAggregator {
	return &EventAggregator{
		events:   make(map[string]*aggregatedEvent),
		cooldown: cooldown,
		watcher:  w,
	}
}

// addEvent adds an event to the aggregator
func (ea *EventAggregator) addEvent(event WatchEvent) {
	if ea.isClosed.Load() {
		return
	}

	ea.mu.Lock()
	defer ea.mu.Unlock()

	now := time.Now()

	existing, exists := ea.events[event.Path]
	if exists {
		// Stop the existing timer
		if existing.timer != nil {
			existing.timer.Stop()
		}

		// Update existing event
		existing.lastSeen = now
		if event.ID > existing.eventID {
			existing.eventID = event.ID
		}

		for _, t := range event.Types {
			existing.types[t] = struct{}{}
		}
		for _, f := range event.Flags {
			existing.flags[f] = struct{}{}
		}
	} else {
		// Create a new aggregated event
		types := make(map[EventType]struct{}, len(event.Types))
		for _, t := range event.Types {
			types[t] = struct{}{}
		}
		flags := make(map[string]struct{}, len(event.Flags))
		for _, f := range event.Flags {
			flags[f] = struct{}{}
		}

		existing = &aggregatedEvent{
			path:     event.Path,
			types:    types,
			flags:    flags,
			lastSeen: now,
			eventID:  event.ID,
		}
		ea.events[event.Path] = existing
	}

	// Schedule or reschedule the flush for this specific path
	path := event.Path
	existing.timer = time.AfterFunc(ea.cooldown, func() {
		ea.flushPath(path)
	})
}

// flushPath sends a single aggregated event and removes it from the map
func (ea *EventAggregator) flushPath(path string) {
	ea.mu.Lock()
	aggregated, exists := ea.events[path]
	if !exists {
		ea.mu.Unlock()
		return
	}

	// Remove from map before sending to avoid race with new events
	delete(ea.events, path)
	ea.mu.Unlock()

	// Collect unique types and flags
	types := make([]EventType, 0, len(aggregated.types))
	for t := range aggregated.types {
		types = append(types, t)
	}

	flags := make([]string, 0, len(aggregated.flags))
	for f := range aggregated.flags {
		flags = append(flags, f)
	}

	ea.watcher.sendToChannel(WatchEvent{
		ID:    aggregated.eventID,
		Path:  aggregated.path,
		Types: types,
		Flags: flags,
		Time:  aggregated.lastSeen,
	})
}

// close stops the aggregator and flushes any remaining events
func (ea *EventAggregator) close() {
	if !ea.isClosed.CompareAndSwap(false, true) {
		return
	}

	ea.mu.Lock()
	// Copy paths to avoid modification during iteration
	paths := make([]string, 0, len(ea.events))
	for path, aggregated := range ea.events {
		if aggregated.timer != nil {
			aggregated.timer.Stop()
		}
		paths = append(paths, path)
	}
	ea.mu.Unlock()

	// Flush everything immediately
	for _, path := range paths {
		ea.flushPath(path)
	}
}

// uniqueEventTypes removes duplicate EventType values from a slice
func uniqueEventTypes(s []EventType) []EventType {
	if len(s) < 2 {
		return s
	}

	// The max value is EventOverflow (6)
	const maxEventType = EventOverflow + 1
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
