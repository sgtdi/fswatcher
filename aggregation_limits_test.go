package fswatcher

import (
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAggregationMaxWaitDeliversContinuouslyChangingFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxWait = 200 * time.Millisecond
		const interval = 5 * time.Millisecond
		w := &watcher{events: make(chan WatchEvent, 1), maxWait: maxWait}
		ea := newEventAggregator(w, 100*time.Millisecond)
		defer ea.close()
		start := time.Now()
		ea.addEvent(WatchEvent{Path: "hot-file", Types: []EventType{EventMod}})
		for elapsed := interval; elapsed < maxWait; elapsed += interval {
			time.Sleep(interval)
			ea.addEvent(WatchEvent{Path: "hot-file", Types: []EventType{EventMod}})
		}
		synctest.Wait()
		require.Empty(t, w.events, "continuous changes must postpone delivery until max wait")
		time.Sleep(interval)
		synctest.Wait()
		require.Len(t, w.events, 1, "continuous changes must not postpone delivery beyond max wait")
		require.Equal(t, maxWait, time.Since(start))
		require.Equal(t, "hot-file", (<-w.events).Path)
		require.Equal(t, int64(39), w.Stats().EventsCoalesced)
	})
}

func TestAggregationMaxWaitCanBeShorterThanCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxWait = 10 * time.Millisecond
		w := &watcher{events: make(chan WatchEvent, 1), maxWait: maxWait}
		ea := newEventAggregator(w, time.Second)
		defer ea.close()
		ea.addEvent(WatchEvent{Path: "file", Types: []EventType{EventMod}})
		time.Sleep(maxWait - time.Millisecond)
		synctest.Wait()
		require.Empty(t, w.events, "event must remain pending until max wait")
		time.Sleep(time.Millisecond)
		synctest.Wait()
		require.Len(t, w.events, 1, "single event ignored max wait")
		require.Equal(t, "file", (<-w.events).Path)
	})
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

func TestAggregationEvictionDoesNotFlushRecreatedPathEarly(t *testing.T) {
	w := &watcher{events: make(chan WatchEvent, 3), maxPending: 1}
	ea := newEventAggregator(w, time.Hour)
	defer ea.close()
	ea.addEvent(WatchEvent{Path: "x", Types: []EventType{EventCreate}})
	ea.mu.Lock()
	oldVersion := ea.events["x"].version
	ea.mu.Unlock()
	ea.addEvent(WatchEvent{Path: "y", Types: []EventType{EventCreate}})
	ea.addEvent(WatchEvent{Path: "x", Types: []EventType{EventMod}})
	ea.flushPath("x", oldVersion, false)
	require.Len(t, w.events, 2, "a stale timer must not flush a new generation at the same path")
	ea.close()
	require.Len(t, w.events, 3)
}
