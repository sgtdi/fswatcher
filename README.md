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

1.  **FSEvents (Default):** Uses the native macOS FSEvents framework. Recommended for macOS — provides the most efficient and comprehensive monitoring. Requires CGO (`CGO_ENABLED=1`)
2.  **kqueue (Pure Go):** Uses the BSD `kernel queue` notification interface. Allows building a static binary without CGO (`CGO_ENABLED=0`), with a shared backend usable on FreeBSD, OpenBSD, NetBSD, and DragonFly

**Why use FSEvents (CGO)?**
The `FSEvents` API is designed specifically for file system monitoring and includes OS-level coalescing. This makes it significantly more accurate and CPU-efficient than `kqueue` for high-volume operations. Benchmarks show `FSEvents` achieves 100% path detection accuracy where **`kqueue` may miss up to 30% of events** for short-lived files under heavy load.

To use the `kqueue` backend on macOS, disable CGO when building:

```bash
CGO_ENABLED=0 go build
```

The library automatically selects the correct implementation based on the build tags. For BSD systems, `kqueue` is the default and only backend.

## Why FSWatcher

Most Go file watchers give you raw OS events—which means duplicate events and noise from system files. FSWatcher solves this:

- **Built-in debouncing** - Merge rapid-fire events automatically
- **Smart filtering** - Regex patterns + automatic exclusion of platform-specific system and temporary files, customizable with `WithIgnoreList`
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
| `WithPath(path, ...)`       | Adds an initial directory to watch. Can be called multiple times. Accepts optional `PathOption` values like `WithDepth`, `WithEventMask`, `WithPathIncRegex`, etc | Current directory |
| `WithCooldown(d)`           | Sets the debouncing cooldown period. Events for the same path arriving within this duration will be merged | `100ms` |
| `WithBufferSize(size)`      | Sets the size of the main event channel | `4096` |
| `WithIncRegex(patterns...)` | Global include regex patterns — only matching paths are processed. If not set, all non-excluded paths are processed | (none) |
| `WithExcRegex(patterns...)` | Global exclude regex patterns — matching paths are ignored. Exclusions take precedence over inclusions | (none) |
| `WithIgnoreList(ignores)`   | Sets the ignored system and temporary files: a `map[string]struct{}` of [`filepath.Match`](https://pkg.go.dev/path/filepath#Match) patterns for base names. Start from `DefaultIgnores()` to add or delete patterns, e.g. delete `"*.plist"` to watch `.plist` files on macOS | `DefaultIgnores()` |
| `WithSeverity(level)`       | Sets the logging verbosity (`SeverityDebug`, `SeverityInfo`, `SeverityWarn`, `SeverityError`) | `SeverityWarn` |
| `WithLogFile(path)`         | Sets a file for logging. Use `"stdout"` to log to the console or `""` to disable | (disabled) |
| `WithLinuxPlatform(p)`      | Sets a specific backend (`PlatformInotify` or `PlatformFanotify`) on Linux | `PlatformInotify` |
| `WithDepth(depth)`          | Sets the watch depth for a path (`WatchNested` or `WatchTopLevel`). Passed to `WithPath` | `WatchNested` |
| `WithEventMask(types...)`   | Filters event types for a specific path. Only the listed types are forwarded to `Events()`, everything else is dropped. Passed to `WithPath` | (all types) |
| `WithPathIncRegex(patterns...)` | Include regex patterns scoped to a specific path, independent of the global `WithIncRegex`. Passed to `WithPath` | (none) |
| `WithPathExcRegex(patterns...)` | Exclude regex patterns scoped to a specific path, independent of the global `WithExcRegex`. Passed to `WithPath` | (none) |
| `WithPathFilter(filter)`    | Custom `PathFilter` implementation for a specific path. Passed to `WithPath` | (none) |

## Methods

Once you have a `Watcher` instance from `New()`, you can use the following methods to control it:

| Method | Description |
| :--- | :--- |
| `Watch(ctx)` | Starts the watcher. Blocking — runs until the context is canceled. Run in a goroutine. A watcher instance is single-use: after `Watch()` returns, create a new watcher with `New()` |
| `Events()` | Returns a read-only channel (`<-chan WatchEvent`) for receiving file system events |
| `AddPath(path)` | Adds a new directory to monitor at runtime. Safe to call concurrently with `DropPath()` |
| `DropPath(path)` | Stops monitoring a directory at runtime. Safe to call concurrently with `AddPath()` |
| `Close()` | Initiates a graceful shutdown — alternative to canceling the context |
| `IsRunning()` | Returns `true` if `Watch()` is currently running |
| `Stats()` | Returns a `WatcherStats` struct with runtime statistics (uptime, events processed, etc.) |
| `Dropped()` | Returns a read-only channel for events dropped because the main `Events()` channel was full |

## Logging

FSWatcher includes a built-in structured logger to help with debugging and monitoring. You can control the verbosity and output destination using `WatcherOpt` functions.

### Log Severity

| Level | Description |
| :--- | :--- |
| `SeverityNone` | No messages logged |
| `SeverityError` | 🚨 Only critical errors (e.g., platform failures) |
| `SeverityWarn` | ⚠️ Errors and warnings (e.g., event queue overflows) — default |
| `SeverityInfo` | ℹ️ Errors, warnings, and informational messages (e.g., watcher start/stop, paths added/removed) |
| `SeverityDebug` | 🐛 Everything, including detailed event processing steps (raw events, filtering, debouncing) |

## Workflow

The watcher operates in a clear, multi-stage pipeline. Each raw OS event goes through:

| Stage | Description |
| :--- | :--- |
| **OS API** | The OS (`FSEvents`, `inotify`, `ReadDirectoryChangesW`) captures a raw file system event |
| **Filtering** | The path is checked against system file rules and regex patterns. Excluded paths are dropped |
| **Debouncing** | The event is held for the cooldown period. Subsequent events for the same path are merged. The timer resets on each new event |
| **User channel** | The final, clean `WatchEvent` is sent to `Events()` |

## Event Aggregation

FSWatcher uses an `EventAggregator` to handle the noise of many OS events. For example, when you save a file, the OS might emit multiple "Edit" and "Chmod" events in rapid succession.

The `EventAggregator` works by:
1.  **Deduplication** — multiple events for the same path within the cooldown window are merged
2.  **Type collection** — the final `WatchEvent` contains all unique `EventType` values (e.g., both `Create` and `Edit` if both occurred)
3.  **Flag collection** — all platform-specific flags are collected and merged
4.  **Deterministic ordering** — aggregated `Types` are sorted by enum order and `Flags` are sorted lexicographically
5.  **Timer-based flushing** — an event is only sent after no new events for that path have arrived within the cooldown window

## Project structure

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

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sgtdi/fswatcher"
)

func main() {

	fsw, err := fswatcher.New(
		fswatcher.WithPath("./"),
		fswatcher.WithSeverity(fswatcher.SeverityDebug),
	)
	if err != nil {
		log.Fatalf("Failed to create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := fsw.Watch(ctx); err != nil && err != context.Canceled {
			log.Printf("Watcher error: %v", err)
		}
	}()

	for event := range fsw.Events() {
		fmt.Printf("Received event:\n%s", event.String())
	}
}
```

### Per-path event filtering

Use `WithEventMask` to receive only specific event types on a given path, and `WithPathIncRegex` / `WithPathExcRegex` to scope regex filters to individual directories:

```go
fsw, err := fswatcher.New(
    // Watch uploads — only care about new files and deletions, ignore edits
    fswatcher.WithPath("/var/uploads",
        fswatcher.WithEventMask(fswatcher.EventCreate, fswatcher.EventRemove),
    ),
    // Watch config — only .yaml files, all event types
    fswatcher.WithPath("/etc/myapp",
        fswatcher.WithPathIncRegex(`\.ya?ml$`),
    ),
)
```

## FAQ

**1. Why create another file watcher?**

> FSWatcher was built to provide features like built-in debouncing and powerful filtering out-of-the-box, which often require manual implementation in other libraries. It also uses a modern Go API with functional options and context-based lifecycle management

**2. How does it handle a large number of files?**

> It uses native OS APIs, which are highly efficient and do not rely on polling. This allows it to watch directories with hundreds of thousands of files without significant performance degradation, limited only by available system memory and OS-specific limits on file handles

**3. What happens if the event buffer is full?**

> If the main event channel is full, the watcher drops the oldest event and records it in the `Dropped()` channel. This prevents blocking the event processing pipeline under heavy load

**4. Can I watch files, or only directories?**

> FSWatcher watches directories only. This ensures consistent, predictable behavior across all platforms. Be aware that the Linux `inotify` backend can struggle with very large or deep directory trees
