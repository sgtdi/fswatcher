[![Go Reference](https://pkg.go.dev/badge/github.com/sgtdi/fswatcher.svg)](https://pkg.go.dev/github.com/sgtdi/fswatcher)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgtdi/fswatcher)](https://goreportcard.com/report/github.com/sgtdi/fswatcher)
[![CI](https://github.com/sgtdi/fswatcher/actions/workflows/ci-test.yml/badge.svg)](https://github.com/sgtdi/fswatcher/actions/workflows/ci-test.yml)
[![CodeQL](https://github.com/sgtdi/fswatcher/actions/workflows/codeql.yml/badge.svg)](https://github.com/sgtdi/fswatcher/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

# FSWatcher

**A production-ready file watcher for Go with built-in debouncing and filtering.**

FSWatcher is a robust, concurrent, and cross-platform file system watcher for Go. It provides a simple and powerful API to monitor directories for file system changes, designed for high-performance applications and development tools. The goal is to abstract away the complexities of platform-specific APIs, offering a unified, easy-to-use, and dependency-free interface.

## Table of contents

- [Supported platforms](#platforms)
- [Why FSWatcher](#why-fswatcher)
- [Quick start](#quick-start)
- [What makes it different](#what-makes-it-different)
- [Options](#options)
- [Methods](#methods)
- [Logging](#logging)
- [Workflow diagram](#workflow)
- [Project structure](#project-structure)
- [Advanced usage](#advanced-usage)
- [FAQ](#faq)

## Platforms

FSWatcher uses native OS APIs for efficient, low-overhead monitoring with near-zero CPU usage when idle.

| Platform | Native System API Used | Status |
| :--- | :--- | :--- |
| ✅ **macOS** | `FSEvents` framework | Default (requires CGO) |
| | `kqueue` | Supported (Pure Go, no CGO) |
| ✅ **BSD** (FreeBSD, OpenBSD, NetBSD, DragonFly) | `kqueue` | Fully supported (Pure Go) |
| ✅ **Linux** | `inotify` | Fully supported |
| | `fanotify` | Partial support (planned for future enhancements) |
| ✅ **Windows** | `ReadDirectoryChangesW` | Fully supported |

### macOS and BSD Support

On macOS, FSWatcher supports two backends:

1.  **FSEvents (Default):** Uses the native macOS FSEvents framework. This is the recommended backend for macOS as it provides the most efficient and comprehensive monitoring. It requires CGO (`CGO_ENABLED=1`).
2.  **kqueue (Pure Go):** Uses the BSD `kernel queue` notification interface. This allows to build a static binary without CGO (`CGO_ENABLED=0`), with a shared backend that can be used with other BSD systems (FreeBSD, OpenBSD, NetBSD, DragonFly).

**Why use FSEvents (CGO)?**
The `FSEvents` API is designed specifically for file system monitoring and includes OS-level coalescing. This makes it significantly more accurate and CPU-efficient than `kqueue` for high-volume operations. Benchmarks show `FSEvents` achieves 100% path detection accuracy where **`kqueue` may miss up to 30% of events** for short-lived files under heavy load.

To use the `kqueue` backend on macOS, simply disable CGO when building your application:

```bash
CGO_ENABLED=0 go build
```

The library automatically selects the correct implementation based on the build tags. For BSD systems, `kqueue` is the default and only backend (CGO is not required).

## Why FSWatcher

Most Go file watchers give you raw OS events—which means duplicate events and noise from system files. FSWatcher solves this:

- **Built-in debouncing** - Merge rapid-fire events automatically
- **Smart filtering** - Regex patterns + automatic system file exclusion (`.git`, `.DS_Store`, etc.)
- **Zero dependencies** - Just standard library + native OS APIs
- **Context-based** - Modern Go patterns with graceful shutdown


## Quick Start

To add FSWatcher to your project, use `go get`:

```sh
go get github.com/sgtdi/fswatcher
```

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/sgtdi/fswatcher"
)

func main() {
    // Create watcher with debouncing that watches the current working directory
    w, _ := fswatcher.New(
        fswatcher.WithCooldown(200*time.Millisecond),
    )

    ctx := context.Background()
    go w.Watch(ctx)
    fmt.Println("fswatcher started, change a file in watcher dir")

    // Process clean, debounced events
    for event := range w.Events() {
        var types, flags []string
        // Loop through types and flags
        for _, t := range event.Types {
            types = append(types, t.String())
        }
        for _, f := range event.Flags {
            flags = append(flags, f)
        }
        fmt.Printf("File changed: %s %v %v\n", event.Path, types, flags)
    }
}
```

## What makes it different

| Feature | FSWatcher | fsnotify / notify |
|---------|-----------|-------------------|
| Debouncing | Built-in, configurable | Manual implementation |
| Path filtering | Regex include/exclude | Manual implementation |
| System files | Auto-ignored | Manual filtering |
| API style | Functional options | Imperative |
| Duplicate events | Handled automatically | Manual deduplication |

## Options

Customize the watcher's behavior using functional options passed to `fswatcher.New()`.

| Option                      | Description | Default |
|:----------------------------| :--- | :--- |
| `WithPath(path, ...)`       | Adds an initial directory to watch. Must be a valid directory path. Can be called multiple times to watch multiple directories. Takes optional `PathOption` values, such as `WithDepth(WatchTopLevel)` to disable recursive watching for that specific path. | Current directory |
| `WithCooldown(d)`           | Sets the debouncing cooldown period. Events for the same path arriving within this duration will be merged. | `100ms` |
| `WithBufferSize(size)`      | Sets the size of the main event channel. | `4096` |
| `WithIncRegex(patterns...)` | Sets a slice of regex patterns for paths to include. If a path matches any of these patterns, it will be processed. If this option is not used, all non-excluded paths are processed by default. | (none) |
| `WithExcRegex(patterns...)` | Sets a slice of regex patterns for paths to exclude. If a path matches any of these patterns, it will be ignored. Exclusions always take precedence over inclusions. | (none) |
| `WithSeverity(level)`       | Sets the logging verbosity (`SeverityDebug`, `SeverityInfo`, `SeverityWarn`, `SeverityError`). | `SeverityWarn` |
| `WithLogFile(path)`         | Sets a file for logging. Use `"stdout"` to log to the console or `""` to disable. | (disabled) |
| `WithLinuxPlatform(p)`      | Sets a specific backend (`PlatformInotify` or `PlatformFanotify`) on Linux. | `PlatformInotify` |
| `WithDepth(depth)`          | Sets the watch depth for a specific path (`WatchNested` or `WatchTopLevel`). This option is passed to `WithPath`. | `WatchNested` |

## Methods

Once you have a `Watcher` instance from `New()`, you can use the following methods to control it:

| Method | Description |
| :--- | :--- |
| `Watch(ctx)` | Starts the watcher. This is a **blocking** call that runs until the provided `context.Context` is canceled. It should almost always be run in a separate goroutine. |
| `Events()` | Returns a read-only channel (`<-chan WatchEvent`) where you receive file system events. You should range over this channel in a goroutine to process events. |
| `AddPath(path)` | Adds a new directory path for the watcher to monitor at runtime. |
| `DropPath(path)` | Stops monitoring a directory path at runtime. |
| `Close()` | Initiates a graceful shutdown of the watcher. This is an alternative to canceling the context passed to `Watch()`. |
| `IsRunning()` | Returns `true` if the watcher's `Watch()` method is currently running. |
| `Stats()` | Returns a `WatcherStats` struct containing runtime statistics like uptime and the number of events processed. |
| `Dropped()` | Returns a read-only channel that receives events that were dropped because the main `Events()` channel was full. |

## Logging

FSWatcher includes a built-in structured logger to help with debugging and monitoring. You can control the verbosity and output destination using `WatcherOpt` functions.

### Log Severity

The log severity determines the minimum severity of messages that will be logged. The available levels are:

| Level | Description |
| :--- | :--- |
| `SeverityNone` | No messages will be logged. |
| `SeverityError` | 🚨 Only critical errors will be logged (e.g., platform failures). |
| `SeverityWarn` | ⚠️ Errors and warnings will be logged (e.g., event queue overflows). This is the default. |
| `SeverityInfo` | ℹ️ Errors, warnings, and informational messages will be logged (e.g., watcher start/stop, paths added/removed). |
| `SeverityDebug` | 🐛 The most verbose level. Logs all messages, including detailed event processing steps (e.g., raw events, filtering, debouncing). |

## Workflow

The watcher operates in a clear, multi-stage pipeline that processes events concurrently. Each raw event from the OS goes through the following stages:

| Stage | Description |
| :--- | :--- |
| **OS API** | The OS (`FSEvents`, `inotify`, `ReadDirectoryChangesW`) captures a raw file system event (e.g., a file was written to). |
| **Filtering** | The event's path is checked against system file rules and user-defined regex patterns (`WithPath`, `WithIncRegex`, `WithExcRegex`). If it's a match for exclusion, it's dropped. |
| **Debouncing** | The event is held for a configurable cooldown period (`WithCooldown`). If another event for the same path arrives during this time, they are aggregated into a single event containing all unique types and flags. The cooldown timer is reset on each new event. |
| **User channel** | The final, clean `WatchEvent` is sent to the `Events()` channel for your application to consume. |

This entire process ensures that your application receives high-quality, actionable events without the noise typically associated with raw file system notifications.

## Event Aggregation

FSWatcher uses an `EventAggregator` to handle the noise of many OS events. For example, when you save a file, the OS might emit multiple "Edit" and "Chmod" events in rapid succession.

The `EventAggregator` works by:
1.  **Deduplication:** Multiple events for the same path within the `WithCooldown` window are merged.
2.  **Type Collection:** The final `WatchEvent` contains all unique `EventType` values that occurred during the window (e.g., if a file was created and then modified, you'll get one event with both `Create` and `Edit` types).
3.  **Flag Collection:** All platform-specific flags are also collected and merged.
4.  **Timer-based Flushing:** A path's aggregated event is only sent to the `Events()` channel after no new events for that path have been received for the duration of the cooldown.

## Project structure

The project is designed to be lightweight and easy to understand, with a clear separation between the core logic and platform-specific implementations.

```
.
├── watcher.go                 # Core watcher logic and public API
├── options.go                 # Configuration options (functional pattern)
├── event.go                   # Event definitions and aggregation logic
├── filters.go                 # Path filtering logic
├── logs.go                    # Logging helpers
├── errors.go                  # Custom error types
├── watcher_darwin.go          # macOS (Pure Go bridge)
├── watcher_darwin_fsevents.go # macOS (FSEvents CGO) implementation
├── watcher_kqueue.go          # kqueue implementation (macOS no-cgo & BSD)
├── watcher_bsd.go             # BSD-specific initialization
├── watcher_linux.go           # Linux platform loader
├── watcher_linux_inotify.go   # Linux (inotify) implementation
├── watcher_linux_fanotify.go  # Linux (fanotify) placeholder
├── watcher_windows.go         # Windows (ReadDirectoryChangesW) implementation
├── go.mod                     # Go module definition
└── examples/
    └── main.go                # Example usage
```

## Advanced usage

This more advanced example shows how to configure the watcher with a specific path and log level, run it in a goroutine, and handle events in a `select` loop.

```go
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
		fswatcher.WithPath("./"), // Set the path to watch, you can add multiple paths
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
```

## FAQ

**1. Why create another file watcher?**

> FSWatcher was built to provide features like built-in debouncing and powerful filtering out-of-the-box, which often require manual implementation in other libraries. It also uses a modern Go API with functional options and context-based lifecycle management.

**2. How does it handle a large number of files?**

> It uses native OS APIs, which are highly efficient and do not rely on polling. This allows it to watch directories with hundreds of thousands of files without significant performance degradation, limited only by available system memory and OS-specific limits on file handles.

**3. What happens if the event buffer is full?**

> If the main event channel is full, the watcher will drop the oldest event and record it in a separate `dropped` events channel, which you can access via `watcher.Dropped()`. This prevents blocking the event processing pipeline under heavy load.

**4. Can I watch files, or only directories?**

> FSWatcher's API is designed to watch directories. This is to ensure consistent, predictable behavior across all platforms (macOS, Windows, and Linux). It is important to be aware of the limitations of the underlying Linux `inotify` backend cause it can struggle with very large or deep directory trees
