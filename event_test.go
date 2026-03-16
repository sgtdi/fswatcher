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
			hasCreate := false
			hasMod := false
			for _, tp := range ev.Types {
				if tp == EventCreate {
					hasCreate = true
				}
				if tp == EventMod {
					hasMod = true
				}
			}
			if !hasCreate || !hasMod {
				t.Errorf("Merged event missing types: Create=%v, Mod=%v", hasCreate, hasMod)
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
}
