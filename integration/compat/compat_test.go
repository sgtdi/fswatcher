package compatibility_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/sgtdi/fswatcher"
	v2 "github.com/sgtdi/fswatcher/v2"
)

func TestVersionsCanWatchConcurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root1, root2 := canonicalTempDir(t), canonicalTempDir(t)
	ready1, ready2 := make(chan struct{}), make(chan struct{})
	w1, err := v1.New(v1.WithPath(root1), v1.WithReadyChannel(ready1), v1.WithCooldown(time.Millisecond), v1.WithSeverity(v1.SeverityNone))
	if err != nil {
		t.Fatal(err)
	}
	w2, err := v2.New(v2.WithPath(root2), v2.WithReadyChannel(ready2), v2.WithCooldown(time.Millisecond), v2.WithSeverity(v2.SeverityNone))
	if err != nil {
		t.Fatal(err)
	}
	result1, result2 := make(chan error, 1), make(chan error, 1)
	go func() { result1 <- w1.Watch(ctx) }()
	go func() { result2 <- w2.Watch(ctx) }()
	t.Cleanup(func() {
		cancel()
		for _, result := range []<-chan error{result1, result2} {
			select {
			case err := <-result:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("watcher did not stop")
			}
		}
	})
	for _, ready := range []<-chan struct{}{ready1, ready2} {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("watcher did not become ready")
		}
	}
	file1, file2 := filepath.Join(root1, "v1.txt"), filepath.Join(root2, "v2.txt")
	for _, file := range []string{file1, file2} {
		if err := os.WriteFile(file, []byte("event"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	waitForEvent(t, ctx, w1.Events(), func(event v1.WatchEvent) bool { return event.Path == file1 })
	waitForEvent(t, ctx, w2.Events(), func(event v2.WatchEvent) bool { return event.Path == file2 })
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForEvent[T any](t *testing.T, ctx context.Context, events <-chan T, matches func(T) bool) {
	t.Helper()
	for {
		select {
		case event, open := <-events:
			if !open {
				t.Fatal("event channel closed before the expected event")
			}
			if matches(event) {
				return
			}
		case <-ctx.Done():
			t.Fatal("watcher did not deliver its event")
		}
	}
}
