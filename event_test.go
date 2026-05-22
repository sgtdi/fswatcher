package fswatcher

import (
	"math/rand"
	"slices"
	"sync"
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

func TestEventAggregator(t *testing.T) {
	t.Run("MergeEvents", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{
			events: events,
		}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"
		now := time.Now()

		// Send first event: Create
		ea.addEvent(WatchEvent{
			Path:  path,
			Types: []EventType{EventCreate},
			Time:  now,
		})

		// Send second event: Mod (within cooldown)
		ea.addEvent(WatchEvent{
			Path:  path,
			Types: []EventType{EventMod},
			Time:  now.Add(10 * time.Millisecond),
		})

		// Wait for more than cooldown
		time.Sleep(cooldown * 2)

		select {
		case ev := <-events:
			if ev.Path != path {
				t.Errorf("Expected path %q, got %q", path, ev.Path)
			}
			if len(ev.Types) != 2 {
				t.Errorf("Expected 2 event types, got %d", len(ev.Types))
			}
			// Check if both types are present
			if !slices.Contains(ev.Types, EventCreate) || !slices.Contains(ev.Types, EventMod) {
				t.Errorf("Merged event missing types: Create=%v, Mod=%v",
					slices.Contains(ev.Types, EventCreate), slices.Contains(ev.Types, EventMod))
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timeout waiting for aggregated event")
		}
	})

	t.Run("IndependentPaths", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{
			events: events,
		}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path1 := "/test/file1.txt"
		path2 := "/test/file2.txt"

		ea.addEvent(WatchEvent{Path: path1, Types: []EventType{EventCreate}})
		ea.addEvent(WatchEvent{Path: path2, Types: []EventType{EventCreate}})

		time.Sleep(cooldown * 2)

		if len(events) != 2 {
			t.Errorf("Expected 2 events in channel, got %d", len(events))
		}
	})

	t.Run("CloseFlushes", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{
			events: events,
		}
		cooldown := 1 * time.Second // Long cooldown
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventCreate}})

		// Close immediately should flush the event
		ea.close()

		select {
		case ev := <-events:
			if ev.Path != path {
				t.Errorf("Expected path %q, got %q", path, ev.Path)
			}
		default:
			t.Fatal("Event was not flushed on close")
		}
	})

	t.Run("TimerReset", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		cooldown := 60 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"

		// First event at t=0
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventCreate}})

		// Second event at t=40ms resets the cooldown timer
		time.Sleep(40 * time.Millisecond)
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventMod}})

		// At t=70ms the original timer would have fired — channel must still be empty
		time.Sleep(30 * time.Millisecond)
		if len(events) != 0 {
			t.Fatal("event was flushed before the reset cooldown expired")
		}

		// At t=120ms the reset timer fires
		time.Sleep(60 * time.Millisecond)
		if len(events) != 1 {
			t.Fatalf("expected 1 merged event after reset cooldown, got %d", len(events))
		}
	})

	t.Run("StaleGenerationDoesNotFlushUpdatedEvent", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		ea := newEventAggregator(w, time.Hour)
		defer ea.close()

		path := "/test/file.txt"
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventCreate}})

		ea.mu.Lock()
		staleVersion := ea.events[path].version
		ea.mu.Unlock()

		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventMod}})
		ea.flushPath(path, staleVersion, false)

		if len(events) != 0 {
			t.Fatal("stale generation flushed an updated event")
		}

		ea.close()
		select {
		case ev := <-events:
			if !slices.Contains(ev.Types, EventCreate) || !slices.Contains(ev.Types, EventMod) {
				t.Fatalf("expected merged event after forced close flush, got %v", ev.Types)
			}
		default:
			t.Fatal("expected forced close flush to deliver updated event")
		}
	})

	t.Run("EventIDTracking", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"
		ea.addEvent(WatchEvent{ID: 5, Path: path, Types: []EventType{EventCreate}})
		ea.addEvent(WatchEvent{ID: 10, Path: path, Types: []EventType{EventMod}})
		ea.addEvent(WatchEvent{ID: 3, Path: path, Types: []EventType{EventChmod}})

		time.Sleep(cooldown * 2)

		select {
		case ev := <-events:
			if ev.ID != 10 {
				t.Errorf("expected highest event ID 10, got %d", ev.ID)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout waiting for aggregated event")
		}
	})

	t.Run("FlagsDeduplication", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventCreate}, Flags: []string{"flag_a", "flag_b"}})
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventMod}, Flags: []string{"flag_b", "flag_c"}})

		time.Sleep(cooldown * 2)

		select {
		case ev := <-events:
			if len(ev.Flags) != 3 {
				t.Errorf("expected 3 unique flags, got %d: %v", len(ev.Flags), ev.Flags)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout waiting for aggregated event")
		}
	})

	t.Run("DeterministicOrdering", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"
		ea.addEvent(WatchEvent{
			Path:  path,
			Types: []EventType{EventRename, EventCreate},
			Flags: []string{"zeta", "alpha"},
		})
		ea.addEvent(WatchEvent{
			Path:  path,
			Types: []EventType{EventMod},
			Flags: []string{"beta"},
		})

		time.Sleep(cooldown * 2)

		select {
		case ev := <-events:
			if len(ev.Types) != 3 {
				t.Fatalf("expected 3 types, got %d", len(ev.Types))
			}
			expectedTypes := []EventType{EventCreate, EventMod, EventRename}
			for i := range expectedTypes {
				if ev.Types[i] != expectedTypes[i] {
					t.Fatalf("types not sorted deterministically: got %v want %v", ev.Types, expectedTypes)
				}
			}

			expectedFlags := []string{"alpha", "beta", "zeta"}
			if len(ev.Flags) != len(expectedFlags) {
				t.Fatalf("expected %d flags, got %d", len(expectedFlags), len(ev.Flags))
			}
			for i := range expectedFlags {
				if ev.Flags[i] != expectedFlags[i] {
					t.Fatalf("flags not sorted deterministically: got %v want %v", ev.Flags, expectedFlags)
				}
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout waiting for aggregated event")
		}
	})

	t.Run("ClosedGuard", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		ea.close()

		// addEvent after close should be silently ignored
		ea.addEvent(WatchEvent{Path: "/test/file.txt", Types: []EventType{EventCreate}})

		time.Sleep(cooldown * 2)

		if len(events) != 0 {
			t.Errorf("expected 0 events after close, got %d", len(events))
		}
	})

	t.Run("ConcurrentSafety", func(t *testing.T) {
		events := make(chan WatchEvent, 1000)
		w := &watcher{events: events}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		paths := []string{"/test/a.txt", "/test/b.txt", "/test/c.txt"}
		const goroutines = 10
		const eventsPerGoroutine = 20

		var wg sync.WaitGroup
		for i := range goroutines {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := range eventsPerGoroutine {
					path := paths[(id+j)%len(paths)]
					ea.addEvent(WatchEvent{
						ID:    uint64(id*eventsPerGoroutine + j),
						Path:  path,
						Types: []EventType{EventMod},
					})
					time.Sleep(time.Millisecond)
				}
			}(i)
		}
		wg.Wait()

		time.Sleep(cooldown * 3)

		if len(events) == 0 {
			t.Error("expected at least one event from concurrent adds")
		}
	})

	t.Run("OverflowEventType", func(t *testing.T) {
		events := make(chan WatchEvent, 10)
		w := &watcher{events: events}
		cooldown := 50 * time.Millisecond
		ea := newEventAggregator(w, cooldown)

		path := "/test/file.txt"
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventCreate}})
		ea.addEvent(WatchEvent{Path: path, Types: []EventType{EventOverflow}})

		time.Sleep(cooldown * 2)

		select {
		case ev := <-events:
			if !slices.Contains(ev.Types, EventOverflow) {
				t.Errorf("EventOverflow was not preserved in aggregated event; got types: %v", ev.Types)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout waiting for aggregated event")
		}
	})
}

func TestEventType_String(t *testing.T) {
	cases := []struct {
		et   EventType
		want string
	}{
		{EventUnknown, "Unknown"},
		{EventCreate, "Create"},
		{EventRemove, "Delete"},
		{EventMod, "Edit"},
		{EventRename, "Rename"},
		{EventChmod, "Chmod"},
		{EventOverflow, "Overflow"},
		{EventType(99), "Invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.et.String(); got != tc.want {
				t.Errorf("EventType(%d).String() = %q; want %q", tc.et, got, tc.want)
			}
		})
	}
}

func TestUniqueEventTypes(t *testing.T) {
	t.Run("Deduplication", func(t *testing.T) {
		in := []EventType{EventCreate, EventMod, EventCreate, EventMod}
		out := uniqueEventTypes(in)
		if len(out) != 2 {
			t.Errorf("expected 2 unique types, got %d: %v", len(out), out)
		}
	})

	t.Run("SingleElement", func(t *testing.T) {
		in := []EventType{EventCreate}
		out := uniqueEventTypes(in)
		if len(out) != 1 {
			t.Errorf("expected 1 type, got %d", len(out))
		}
	})

	t.Run("Empty", func(t *testing.T) {
		out := uniqueEventTypes(nil)
		if out != nil {
			t.Errorf("expected nil, got %v", out)
		}
	})

	t.Run("OverflowPreserved", func(t *testing.T) {
		in := []EventType{EventCreate, EventOverflow, EventCreate, EventOverflow}
		out := uniqueEventTypes(in)
		if !slices.Contains(out, EventOverflow) {
			t.Error("EventOverflow was silently dropped by uniqueEventTypes; maxEventType must include EventOverflow")
		}
	})
}
