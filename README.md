[![Go Reference](https://pkg.go.dev/badge/github.com/sgtdi/fswatcher.svg)](https://pkg.go.dev/github.com/sgtdi/fswatcher)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgtdi/fswatcher)](https://goreportcard.com/report/github.com/sgtdi/fswatcher)
[![CI](https://github.com/sgtdi/fswatcher/actions/workflows/ci-test.yml/badge.svg)](https://github.com/sgtdi/fswatcher/actions/workflows/ci-test.yml)
[![CodeQL](https://github.com/sgtdi/fswatcher/actions/workflows/codeql.yml/badge.svg)](https://github.com/sgtdi/fswatcher/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

# FSWatcher

FSWatcher is a robust, concurrent, and cross-platform file system watcher for Go. It provides a simple and powerful API to monitor directories for file system changes, designed for high-performance applications and development tools. The goal is to abstract away the complexities of platform-specific APIs, offering a unified, easy-to-use, and dependency-free interface.

## Table of contents

- [Supported platforms](#platforms)
- [Feature comparison](#feature)
- [Workflow diagram](#workflow)
- [Project structure](#project-structure)
- [Getting started](#getting-started)
- [Configuration options](#configuration-options)
- [Watcher methods](#watcher-methods)
- [Logging](#logging)
- [FAQ](#faq)

## Platforms

FSWatcher uses native OS APIs for efficient, low-overhead monitoring with near-zero CPU usage when idle.

| Platform | Native System API Used | Status |
| :--- | :--- | :--- |
| ✅ **macOS** | `FSEvents` framework | Fully supported |
| ✅ **Linux** | `inotify` | Fully supported |
| | `fanotify` | Partial support (planned for future enhancements) |
| ✅ **Windows** | `ReadDirectoryChangesW` | Fully supported |

## Features

This library was created to address common challenges found in other file system watchers, such as event batching, debouncing, and simplified configuration.

| Feature | FSWatcher | Similar projects |
| :--- | :--- | :--- |
| **Event debouncing** | ✅ Built-in (configurable cooldown) | ❌ Manual implementation required |
| **Event batching** | ✅ Built-in (configurable duration) | ❌ Manual implementation required |
| **Filtering** | ✅ Regex patterns (inc/exc) | ❌ Manual implementation required (often no built-in filtering) |
| **API style** | Functional options, context-managed | Typically imperative, channel-based |
| **System file ignore**| ✅ Automatic (e.g., `.git`, `.DS_Store`) | ❌ Manual implementation required |
| **Logging** | ✅ Built-in structured logging | ❌ Manual implementation required |

## Workflow

The watcher operates in a clear, multi-stage pipeline that processes events concurrently. Each raw event from the OS goes through the following stages:

| Stage | Description |
| :--- | :--- |
| **1. OS Native API** | The OS (`FSEvents`, `inotify`, `ReadDirectoryChangesW`) captures a raw file system event (e.g., a file was written to). |
| **2. Filtering** | The event's path is checked against system file rules and user-defined regex patterns (`WithPath`, `WithIncRegex`, `WithExcRegex`). If it's a match for exclusion, it's dropped. |
| **3. Debouncing** | The event is held for a configurable cooldown period (`WithCooldown`). If another event for the same path arrives during this time, the two events are merged into one. |
| **4. Batching** | If enabled (`WithEventBatching`), the debounced event is held in a batch. The batch is released as a single `WatchEvent` after a configurable duration. |
| **5. User Channel** | The final, clean `WatchEvent` is sent to the `Events()` channel for your application to consume. |

This entire process ensures that your application receives high-quality, actionable events without the noise typically associated with raw file system notifications.

## Project structure

The project is designed to be lightweight and easy to understand, with a clear separation between the core logic and platform-specific implementations.

```
.
├── watcher.go                 # Core watcher logic and public API
├── options.go                 # Configuration options (functional pattern)
├── event.go                   # Event definitions and batching logic
├── debouncer.go               # Event debouncing component
├── filters.go                 # Path filtering logic
├── logs.go                    # Logging helpers
├── errors.go                  # Custom error types
├── watcher_darwin.go          # macOS (FSEvents) implementation
├── watcher_linux.go           # Linux platform loader
├── watcher_linux_inotify.go   # Linux (inotify) implementation
├── watcher_linux_fanotify.go  # Linux (fanotify) placeholder
├── watcher_windows.go         # Windows (ReadDirectoryChangesW) implementation
├── go.mod                     # Go module definition
└── examples/
    └── main.go                # Example usage
```

## Getting started

### Installation

To add FSWatcher to your project, use `go get`:

```sh
go get github.com/sgtdi/fswatcher
```

### Example

Here is a minimal example that monitors the current directory until the program is interrupted (e.g., with `Ctrl+C`).

```go
package main

import (
	"context"
	"fmt"

	"github.com/sgtdi/fswatcher"
)

func main() {
	fsw, _ := fswatcher.New()
	ctx, _ := context.WithCancel(context.Background())
	go fsw.Watch(ctx)
	// Watch for events
	for e := range fsw.Events() {
	    fmt.Println(e.String())
	}
}
```

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
	for {
		select {
		case event, ok := <-fsw.Events():
			if !ok {
				return // Channel closed
			}
			fmt.Printf("Received event:\n%s", event.String())
		}
	}
}
```

## Configuration options

Customize the watcher's behavior using functional options passed to `fswatcher.New()`.

| Option                      | Description | Default |
|:----------------------------| :--- | :--- |
| `WithPath(path, ...)`       | Adds an initial directory to watch. Must be a valid directory path. Can be called multiple times to watch multiple directories. Takes optional `PathOption` values, such as `WithDepth(WatchTopLevel)` to disable recursive watching for that specific path. | Current directory |
| `WithCooldown(d)`           | Sets the debouncing cooldown period. Events for the same path arriving within this duration will be merged. | `100ms` |
| `WithBufferSize(size)`      | Sets the size of the main event channel. | `4096` |
| `WithIncRegex(patterns...)` | Sets a slice of regex patterns for paths to include. If a path matches any of these patterns, it will be processed. If this option is not used, all non-excluded paths are processed by default. | (none) |
| `WithExcRegex(patterns...)` | Sets a slice of regex patterns for paths to exclude. If a path matches any of these patterns, it will be ignored. Exclusions always take precedence over inclusions. | (none) |
| `WithEventBatching(d)`      | Enables and configures event batching. Multiple events for the same path within the duration are merged. | (disabled) |
| `WithSeverity(level)`       | Sets the logging verbosity (`SeverityDebug`, `SeverityInfo`, `SeverityWarn`, `SeverityError`). | `SeverityWarn` |
| `WithLogFile(path)`         | Sets a file for logging. Use `"stdout"` to log to the console or `""` to disable. | (disabled) |
| `WithLinuxPlatform(p)`      | Sets a specific backend (`PlatformInotify` or `PlatformFanotify`) on Linux. | `PlatformInotify` |
| `WithDepth(depth)`          | Sets the watch depth for a specific path (`WatchNested` or `WatchTopLevel`). This option is passed to `WithPath`. | `WatchNested` |

## Watcher methods

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


## FAQ

**1. Why create another file watcher?**

> FSWatcher was built to provide features like built-in debouncing, event batching, and powerful filtering out-of-the-box, which often require manual implementation in other libraries. It also uses a modern Go API with functional options and context-based lifecycle management.

**2. How does it handle a large number of files?**

> It uses native OS APIs, which are highly efficient and do not rely on polling. This allows it to watch directories with hundreds of thousands of files without significant performance degradation, limited only by available system memory and OS-specific limits on file handles.

**3. What happens if the event buffer is full?**

> If the main event channel is full, the watcher will drop the oldest event and record it in a separate `dropped` events channel, which you can access via `watcher.Dropped()`. This prevents blocking the event processing pipeline under heavy load.

**4. Can I watch files, or only directories?**

> FSWatcher's API is designed to watch directories. This is to ensure consistent, predictable behavior across all platforms (macOS, Windows, and Linux). It is important to be aware of the limitations of the underlying Linux `inotify` backend cause it can struggle with very large or deep directory trees
