package fswatcher

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAggregationMaxWaitDeliversContinuouslyChangingFile(t *testing.T) {
	wi, err := New(WithPath(t.TempDir()), WithCooldown(100*time.Millisecond), WithMaxWait(200*time.Millisecond))
	require.NoError(t, err)
	w := wi.(*watcher)
	defer w.aggregator.close()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	w.aggregator.addEvent(WatchEvent{Path: "hot-file", Types: []EventType{EventMod}})
	for {
		select {
		case <-ticker.C:
			w.aggregator.addEvent(WatchEvent{Path: "hot-file", Types: []EventType{EventMod}})
		case event := <-w.Events():
			require.Equal(t, "hot-file", event.Path)
			require.Greater(t, w.Stats().EventsCoalesced, int64(5))
			return
		case <-timeout.C:
			t.Fatal("continuous changes postponed delivery beyond max wait")
		}
	}
}

func TestAggregationMaxWaitCanBeShorterThanCooldown(t *testing.T) {
	wi, err := New(WithPath(t.TempDir()), WithCooldown(time.Second), WithMaxWait(10*time.Millisecond))
	require.NoError(t, err)
	w := wi.(*watcher)
	defer w.aggregator.close()
	w.aggregator.addEvent(WatchEvent{Path: "file", Types: []EventType{EventMod}})
	select {
	case <-w.Events():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("single event ignored max wait")
	}
}

func TestAggregationCapacityFlushesWithoutLosingPaths(t *testing.T) {
	const count = 800
	w := &watcher{events: make(chan WatchEvent, count), maxPending: 2}
	ea := newEventAggregator(w, time.Hour)
	var writers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		writers.Add(1)
		go func(worker int) {
			defer writers.Done()
			for i := 0; i < 100; i++ {
				ea.addEvent(WatchEvent{Path: fmt.Sprintf("%d/%d", worker, i), Types: []EventType{EventCreate}})
			}
		}(worker)
	}
	writers.Wait()
	ea.mu.Lock()
	pending := len(ea.events)
	ea.mu.Unlock()
	require.LessOrEqual(t, pending, 2)
	ea.close()
	require.Len(t, w.events, count)
	seen := make(map[string]bool)
	for len(w.events) > 0 {
		event := <-w.events
		require.False(t, seen[event.Path])
		seen[event.Path] = true
	}
	require.Zero(t, w.Stats().EventsLost)
}

func TestAggregationLimitOptions(t *testing.T) {
	_, err := New(WithPath(t.TempDir()), WithMaxPendingEvents(0))
	require.Error(t, err)
	_, err = New(WithPath(t.TempDir()), WithMaxWait(-time.Second))
	require.Error(t, err)
	wi, err := New(WithPath(t.TempDir()), WithMaxWait(0), WithMaxPendingEvents(2))
	require.NoError(t, err)
	require.Zero(t, wi.(*watcher).aggregator.maxWait)
	require.Equal(t, 2, wi.(*watcher).aggregator.maxPending)
}
