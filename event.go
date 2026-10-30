package fswatcher

import (
	"container/heap"
	"fmt"
	"math/bits"
	"sort"
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
	events    map[string]*aggregatedEvent
	deadlines deadlineHeap
	cooldown  time.Duration
	watcher   *watcher
	mu        sync.Mutex
	isClosed  atomic.Bool
	started   atomic.Bool
	wake      chan struct{}
	done      chan struct{}
}

// aggregatedEvent holds merged data for events on the same path
type aggregatedEvent struct {
	path       string
	typeMask   uint8
	extraTypes map[EventType]struct{}
	flags      map[string]struct{}
	lastSeen   time.Time
	eventID    uint64
	deadline   time.Time
	version    uint64
	heapIndex  int
}

// deadlineHeap keeps the next event to flush at the root, avoiding a scan of
// every pending path whenever the timer is reset or expires.
type deadlineHeap []*aggregatedEvent

func (h deadlineHeap) Len() int           { return len(h) }
func (h deadlineHeap) Less(i, j int) bool { return h[i].deadline.Before(h[j].deadline) }
func (h deadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *deadlineHeap) Push(value any) {
	event := value.(*aggregatedEvent)
	event.heapIndex = len(*h)
	*h = append(*h, event)
}

func (h *deadlineHeap) Pop() any {
	old := *h
	last := len(old) - 1
	event := old[last]
	old[last] = nil
	event.heapIndex = -1
	*h = old[:last]
	return event
}

func (event *aggregatedEvent) addTypes(types []EventType) {
	for _, eventType := range types {
		if eventType <= EventOverflow {
			event.typeMask |= 1 << eventType
			continue
		}
		if event.extraTypes == nil {
			event.extraTypes = make(map[EventType]struct{})
		}
		event.extraTypes[eventType] = struct{}{}
	}
}

func (event *aggregatedEvent) eventTypes() []EventType {
	types := make([]EventType, 0, bits.OnesCount8(event.typeMask)+len(event.extraTypes))
	for eventType := EventUnknown; eventType <= EventOverflow; eventType++ {
		if event.typeMask&(1<<eventType) != 0 {
			types = append(types, eventType)
		}
	}
	for eventType := range event.extraTypes {
		types = append(types, eventType)
	}
	if len(event.extraTypes) > 1 {
		sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	}
	return types
}

// newEventAggregator creates a new event aggregator
func newEventAggregator(w *watcher, cooldown time.Duration) *EventAggregator {
	return &EventAggregator{
		events:   make(map[string]*aggregatedEvent),
		cooldown: cooldown,
		watcher:  w,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// addEvent adds an event to the aggregator
func (ea *EventAggregator) addEvent(event WatchEvent) {
	if ea.isClosed.Load() {
		return
	}
	if ea.started.CompareAndSwap(false, true) {
		go ea.run()
	}

	ea.mu.Lock()
	defer ea.mu.Unlock()
	if ea.isClosed.Load() {
		return
	}

	now := time.Now()
	deadline := now.Add(ea.cooldown)

	existing, exists := ea.events[event.Path]
	if exists {
		// Update existing event
		existing.lastSeen = now
		existing.deadline = deadline
		existing.version++
		if event.ID > existing.eventID {
			existing.eventID = event.ID
		}

		existing.addTypes(event.Types)
		for _, f := range event.Flags {
			if existing.flags == nil {
				existing.flags = make(map[string]struct{})
			}
			existing.flags[f] = struct{}{}
		}
		if existing.heapIndex < 0 {
			heap.Push(&ea.deadlines, existing)
		} else {
			heap.Fix(&ea.deadlines, existing.heapIndex)
		}
	} else {
		existing = &aggregatedEvent{
			path:      event.Path,
			lastSeen:  now,
			eventID:   event.ID,
			deadline:  deadline,
			version:   1,
			heapIndex: -1,
		}
		existing.addTypes(event.Types)
		for _, f := range event.Flags {
			if existing.flags == nil {
				existing.flags = make(map[string]struct{})
			}
			existing.flags[f] = struct{}{}
		}
		ea.events[event.Path] = existing
		heap.Push(&ea.deadlines, existing)
	}

	ea.signal()
}

func (ea *EventAggregator) signal() {
	select {
	case ea.wake <- struct{}{}:
	default:
	}
}

func (ea *EventAggregator) run() {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		next, ok := ea.nextDeadline()
		if !ok {
			if timer != nil {
				timer.Stop()
				timer = nil
				timerC = nil
			}
			select {
			case <-ea.wake:
				continue
			case <-ea.done:
				return
			}
		}

		delay := time.Until(next)
		if delay <= 0 {
			ea.flushDue(time.Now())
			continue
		}
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		}
		timerC = timer.C

		select {
		case <-timerC:
			ea.flushDue(time.Now())
		case <-ea.wake:
		case <-ea.done:
			if timer != nil {
				timer.Stop()
			}
			return
		}
	}
}

func (ea *EventAggregator) nextDeadline() (time.Time, bool) {
	ea.mu.Lock()
	defer ea.mu.Unlock()
	if len(ea.deadlines) == 0 {
		return time.Time{}, false
	}
	return ea.deadlines[0].deadline, true
}

func (ea *EventAggregator) flushDue(now time.Time) {
	var ready []struct {
		path    string
		version uint64
	}

	ea.mu.Lock()
	if ea.isClosed.Load() {
		ea.mu.Unlock()
		return
	}
	for len(ea.deadlines) > 0 {
		event := ea.deadlines[0]
		if !event.deadline.After(now) {
			heap.Pop(&ea.deadlines)
			ready = append(ready, struct {
				path    string
				version uint64
			}{path: event.path, version: event.version})
			continue
		}
		break
	}
	ea.mu.Unlock()

	for _, item := range ready {
		ea.flushPath(item.path, item.version, false)
	}
}

// flushPath sends a single aggregated event and removes it from the map
func (ea *EventAggregator) flushPath(path string, version uint64, force bool) {
	ea.mu.Lock()
	if !force && ea.isClosed.Load() {
		ea.mu.Unlock()
		return
	}

	aggregated, exists := ea.events[path]
	if !exists {
		ea.mu.Unlock()
		return
	}
	if !force && aggregated.version != version {
		ea.mu.Unlock()
		return
	}

	// Remove from map before sending to avoid race with new events
	if aggregated.heapIndex >= 0 {
		heap.Remove(&ea.deadlines, aggregated.heapIndex)
	}
	delete(ea.events, path)

	// Collect unique types and flags
	types := aggregated.eventTypes()

	flags := make([]string, 0, len(aggregated.flags))
	for f := range aggregated.flags {
		flags = append(flags, f)
	}
	sort.Strings(flags)

	event := WatchEvent{
		ID:    aggregated.eventID,
		Path:  aggregated.path,
		Types: types,
		Flags: flags,
		Time:  aggregated.lastSeen,
	}

	ea.mu.Unlock()
	ea.watcher.sendToChannel(event)
}

// close stops the aggregator and flushes any remaining events
func (ea *EventAggregator) close() {
	if !ea.isClosed.CompareAndSwap(false, true) {
		return
	}
	close(ea.done)

	ea.mu.Lock()
	// Copy paths to avoid modification during iteration
	paths := make([]string, 0, len(ea.events))
	for path := range ea.events {
		paths = append(paths, path)
	}
	ea.mu.Unlock()

	// Flush everything immediately
	for _, path := range paths {
		ea.flushPath(path, 0, true)
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
