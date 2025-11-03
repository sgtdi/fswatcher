package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sgtdi/fswatcher"
)

func main() {
	path := "/app"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Fatalf("Error: Watch path %q does not exist", path)
	}

	log.Printf("Setting up watcher for path: %s", path)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create a new watcher with desired options
	w, err := fswatcher.New(
		// Set the entrypoint path
		fswatcher.WithPath(path),
		// Enable batcher for events on the same path that arrive close
		fswatcher.WithEventBatching(10*time.Millisecond),
		// Skip events fired in very close in time
		fswatcher.WithCooldown(200*time.Millisecond),
		// Print every log with the highest level
		fswatcher.WithLogLevel(fswatcher.LogLevelDebug),
	)
	if err != nil {
		log.Fatalf("Failed to create watcher: %v", err)
	}

	// Process events
	go func() {
		events := w.Events()
		dropped := w.Dropped()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					log.Println("Event channel closed")
					return
				}
				fmt.Printf("EVENT: %s\n", event.String())

			case event, ok := <-dropped:
				if !ok {
					log.Println("Dropped channel closed")
					return
				}
				log.Printf("DROPPED EVENT: %s", event.String())

			case <-ctx.Done():
				log.Println("Context canceled")
				return
			}
		}
	}()

	log.Println("Watcher started, press Ctrl+C to exit")
	if err := w.Watch(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Fatalf("Watcher failed with an unexpected error: %v", err)
		}
	}
	log.Println("Watcher stopped")
}
