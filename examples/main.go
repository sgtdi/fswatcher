package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sgtdi/fswatcher"
)

func main() {

	// Create a new fswatcher instance with options
	fsw, err := fswatcher.New(
		fswatcher.WithPath("./"),
		fswatcher.WithSeverity(fswatcher.SeverityDebug),
	)
	if err != nil {
		log.Fatalf("Failed to create watcher: %v", err)
	}

	// Start the watcher in a goroutine
	ctx, _ := context.WithCancel(context.Background())
	go func() {
		log.Println("Watcher started.")
		if err := fsw.Watch(ctx); err != nil && err != context.Canceled {
			log.Printf("Watcher error: %v", err)
		}
		log.Println("Watcher stopped.")
	}()

	// Listen for events or a shutdown signal
	for event := range fsw.Events() {
		fmt.Printf("Received event:\n%s", event.String())
	}
}
