package main

import (
	"context"
	"fmt"

	"github.com/sgtdi/fswatcher"
)

func main() {
	fsw, _ := fswatcher.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fsw.Watch(ctx) }()
	// Watch for events
	for e := range fsw.Events() {
		fmt.Println(e.String())
	}
}
