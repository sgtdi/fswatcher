package fswatcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type deliveryGate struct{ entered, release chan struct{} }

func (*deliveryGate) Enabled(context.Context, slog.Level) bool { return true }
func (h *deliveryGate) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "Raw event" {
		close(h.entered)
		<-h.release
	}
	return nil
}
func (h *deliveryGate) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *deliveryGate) WithGroup(string) slog.Handler      { return h }

func TestAggregatorCloseWaitsForDelivery(t *testing.T) {
	gate := &deliveryGate{entered: make(chan struct{}), release: make(chan struct{})}
	w := &watcher{events: make(chan WatchEvent, 1), logger: slog.New(gate)}
	ea := newEventAggregator(w, time.Millisecond)
	ea.addEvent(WatchEvent{Path: "file", Types: []EventType{EventMod}})
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	closed := make(chan struct{})
	go func() { ea.close(); close(closed) }()
	select {
	case <-closed:
		close(gate.release)
		t.Fatal("close returned before the in-flight delivery")
	case <-time.After(20 * time.Millisecond):
	}
	close(gate.release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not complete")
	}
	close(w.events)
	require.Equal(t, "file", (<-w.events).Path)
}

func TestAddPathCannotFinishAfterShutdown(t *testing.T) {
	ready := make(chan struct{})
	w, err := New(WithPath(t.TempDir()), WithReadyChannel(ready), WithSeverity(SeverityNone))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- w.Watch(ctx) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	other := t.TempDir()
	go func() { result <- w.AddPath(other, func(*WatchPath) { close(entered); <-release }) }()
	<-entered
	cancel()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked by caller options")
	}
	close(release)
	require.ErrorContains(t, <-result, "not running")
	require.NotContains(t, w.Paths(), other)
}

func TestStartupFailureClosesOwnedChannels(t *testing.T) {
	w, err := New(WithPath(t.TempDir()), WithLogFile(filepath.Join(t.TempDir(), "missing", "log")))
	require.NoError(t, err)
	require.Error(t, w.Watch(context.Background()))
	select {
	case _, open := <-w.Events():
		require.False(t, open)
	default:
		t.Fatal("Events left open after startup failure")
	}
	select {
	case _, open := <-w.Dropped():
		require.False(t, open)
	default:
		t.Fatal("Dropped left open after startup failure")
	}
}
