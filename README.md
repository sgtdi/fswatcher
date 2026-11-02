[![Go Reference](https://pkg.go.dev/badge/github.com/sgtdi/fswatcher/v2.svg)](https://pkg.go.dev/github.com/sgtdi/fswatcher/v2)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgtdi/fswatcher)](https://goreportcard.com/report/github.com/sgtdi/fswatcher)
[![CI](https://github.com/sgtdi/fswatcher/actions/workflows/ci-test.yml/badge.svg)](https://github.com/sgtdi/fswatcher/actions/workflows/ci-test.yml)
[![CodeQL](https://github.com/sgtdi/fswatcher/actions/workflows/codeql.yml/badge.svg)](https://github.com/sgtdi/fswatcher/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

# FSWatcher

**A production-ready file watcher for Go with built-in debouncing and filtering.**

FSWatcher is a robust, concurrent, and cross-platform file system watcher for Go. It provides a simple API to monitor directories for file system changes, using native platform APIs through `golang.org/x/sys` and the standard library.

See the [changelog](CHANGELOG.md) for release details and [Updating existing callers](#updating-existing-callers) for the v1-to-v2 migration.

## Table of contents

- [Requirements](#requirements)
- [Supported platforms](#platforms)
- [Why FSWatcher](#why-fswatcher)
- [Quick start](#quick-start)
- [What makes it different](#what-makes-it-different)
- [Options](#options)
- [Updating existing callers](#updating-existing-callers)
- [Methods](#methods)
- [Logging](#logging)
- [Workflow diagram](#workflow)
- [Event aggregation](#event-aggregation)
- [Project structure](#project-structure)
- [Advanced usage](#advanced-usage)
- [Testing](#testing)
- [FAQ](#faq)

## Requirements

Go 1.27.3 or later is required. The default macOS backend also requires CGO and a C compiler; the kqueue backend can be built with `CGO_ENABLED=0`.

The library has one external runtime dependency, `golang.org/x/sys`. `github.com/stretchr/testify` and its YAML dependency are used only by the tests.

## Platforms

FSWatcher uses native OS APIs for efficient, low-overhead monitoring with near-zero CPU usage when idle.

| Platform | Native System API Used | Status |
| :--- | :--- | :--- |
| ✅ **macOS** | `FSEvents` framework | Default (requires CGO) |
| | `kqueue` | Supported (Pure Go, no CGO) |
| ✅ **BSD** (FreeBSD, OpenBSD, NetBSD, DragonFly) | `kqueue` | Fully supported (Pure Go) |
| ✅ **Linux** | `inotify` | Fully supported |
| | `fanotify` | Partial support (recursive directory marks are best-effort and kernel/filesystem dependent) |
| ✅ **Windows** | `ReadDirectoryChangesW` | Fully supported |
| ✅ **iOS** | `kqueue` | Supported (FSEvents is not available on iOS) |
| ⚪ **Other** (js/wasm, wasip1, plan9, ...) | None | Builds; `Watch` returns `ErrUnsupportedPlatform` |

### macOS and BSD Support

On macOS, FSWatcher supports two backends:

1.  **FSEvents (Default):** Uses the native macOS FSEvents framework. Recommended for macOS — provides the most efficient and comprehensive monitoring. Requires CGO (`CGO_ENABLED=1`)
2.  **kqueue (Pure Go):** Uses the BSD `kernel queue` notification interface. Allows building a static binary without CGO (`CGO_ENABLED=0`), with a shared backend usable on FreeBSD, OpenBSD, NetBSD, and DragonFly

**Why use FSEvents (CGO)?**
The `FSEvents` API monitors directory trees and includes OS-level coalescing. The kqueue backend needs a descriptor for each monitored entry and scans changed directories to discover new entries. Files created and removed before that scan can be missed.

To use the `kqueue` backend on macOS, disable CGO when building:

```bash
CGO_ENABLED=0 go build
```

The library automatically selects the correct implementation based on the build tags. For BSD systems, `kqueue` is the default and only backend.

## Why FSWatcher

Most Go file watchers give you raw OS events—which means duplicate events and noise from system files. FSWatcher solves this:

- **Built-in debouncing** - Merge rapid-fire events automatically
- **Smart filtering** - Hidden paths are excluded by default; regex patterns and platform-specific system and temporary files are filtered automatically, with `WithIgnoreList` for customization
- **Lightweight dependencies** - `golang.org/x/sys` for low-level platform APIs, plus the standard library
- **Context-based** - Modern Go patterns with graceful shutdown


## Quick Start

To add FSWatcher to your project, use `go get`:

```sh
go get github.com/sgtdi/fswatcher/v2
```

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/sgtdi/fswatcher/v2"
)

func main() {
	// Watch the current working directory until Ctrl+C.
	w, err := fswatcher.New(fswatcher.WithCooldown(200 * time.Millisecond))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- w.Watch(ctx) }()

	for event := range w.Events() {
		fmt.Printf("File changed: %s %v\n", event.Path, event.Types)
	}
	if err := <-result; err != nil {
		log.Fatal(err)
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

Pass watcher options to `fswatcher.New()`. Pass `PathOption` values, such as `WithDepth` and `WithEventMask`, to `WithPath()` or `AddPath()`.

| Option                      | Description | Default |
|:----------------------------| :--- | :--- |
| `WithPath(path, ...)`       | Adds an existing directory to watch, resolving symbolic links to its canonical absolute path. Can be called multiple times. Accepts optional `PathOption` values like `WithDepth`, `WithEventMask`, `WithPathIncRegex`, etc | Current directory |
| `WithCooldown(d)`           | Sets the debouncing cooldown period. Events for the same path arriving within this duration will be merged | `100ms` |
| `WithMaxWait(d)` | Caps aggregation latency for a path during continuous changes. `0` disables the cap | `max(1s, cooldown)` |
| `WithMaxPendingEvents(n)` | Bounds pending paths; reaching the limit delivers a pending event early | `4096` |
| `WithBufferSize(size)`      | Sets the size of the main event channel | `4096` |
| `WithReadBufferSize(bytes)` | Sets the native read buffer, from 4 KiB to 1 MiB; Windows network watches cap it at 64 KiB | `64 KiB` |
| `WithCustomChannels(events, dropped)` | Supplies caller-owned channels; each `nil` argument uses an internally allocated channel. Keep supplied channels open until `Watch()` returns; FSWatcher leaves them open | Internal channels |
| `WithReadyChannel(ready)` | Closes `ready` after successful backend initialization and registration of the initial paths. Startup failures are returned by `Watch()` | (none) |
| `WithIncRegex(patterns...)` | Global include regex patterns — only matching paths are processed. If not set, all non-excluded paths are processed | (none) |
| `WithExcRegex(patterns...)` | Global exclude regex patterns — matching paths are ignored. Exclusions take precedence over inclusions | (none) |
| `WithHidden()`              | Includes hidden files and directories. By default, dot-prefixed paths are excluded on Unix-like platforms; Windows `FILE_ATTRIBUTE_HIDDEN` and macOS/FreeBSD `UF_HIDDEN` paths are also excluded. System and temporary ignore patterns still apply | Excluded |
| `WithIgnoreList(ignores)`   | Sets the ignored system and temporary files: a `map[string]struct{}` of [`filepath.Match`](https://pkg.go.dev/path/filepath#Match) patterns for base names. Start from `DefaultIgnores()` to add or delete patterns, e.g. delete `"*.plist"` and use `WithHidden()` to watch `.plist` files on macOS | `DefaultIgnores()` |
| `WithSeverity(level)`       | Sets the logging verbosity (`SeverityDebug`, `SeverityInfo`, `SeverityWarn`, `SeverityError`) | `SeverityWarn` |
| `WithLogFile(path)`         | Sets a file for logging. Use `"stdout"` to log to the console or `""` to disable | (disabled) |
| `WithLinuxPlatform(p)`      | Sets a specific backend (`PlatformInotify` or `PlatformFanotify`) on Linux | `PlatformInotify` |
| `WithDepth(depth)`          | Sets the watch depth for a path (`WatchNested` or `WatchTopLevel`). Passed to `WithPath` | `WatchNested` |
| `WithEventMask(types...)`   | Filters event types for a specific path. Only the listed types are forwarded to `Events()`, everything else is dropped. Passed to `WithPath` | (all types) |
| `WithPathIncRegex(patterns...)` | Include regex patterns scoped to a specific path, independent of the global `WithIncRegex`. Passed to `WithPath` | (none) |
| `WithPathExcRegex(patterns...)` | Exclude regex patterns scoped to a specific path, independent of the global `WithExcRegex`. Passed to `WithPath` | (none) |
| `WithPathFilter(filter)`    | Custom `PathFilter` implementation for a specific path. Passed to `WithPath` | (none) |

Global and per-path filters both apply. If roots overlap, the most specific root determines the per-path filter, event mask, and depth. `EventOverflow` bypasses these filters and masks so consumers can reconcile lost changes.

Custom `PathFilter` implementations must support concurrent calls to `ShouldInclude`.

On Windows, a dot-prefixed name alone does not make a path hidden; the filesystem's `FILE_ATTRIBUTE_HIDDEN` attribute is used.

When a macOS or FreeBSD path becomes visible after its native hidden attribute is cleared, normal event delivery resumes, including monitoring descendants of a newly visible directory. Dot-prefixed Unix paths become visible by being renamed; `WithHidden()` includes them without that change.

## Updating existing callers

Version 2 uses the module path `github.com/sgtdi/fswatcher/v2`, following [Go's major-version rules](https://go.dev/doc/modules/major-version). Upgrade the dependency and update every FSWatcher import:

```sh
go get github.com/sgtdi/fswatcher/v2@v2.0.0
```

```go
import "github.com/sgtdi/fswatcher/v2"
```

Run `go mod tidy` in your application after updating imports. Existing v1 tags remain available through `github.com/sgtdi/fswatcher`.

The native CI matrix also verifies that v1 and v2 can watch concurrently in the same application. The isolated compatibility test module under `integration/compat` keeps the legacy version out of the v2 module's dependency graph.

Version 2 requires Go 1.27.3 or later and changes these defaults:

- Hidden paths are excluded according to the platform's convention; use `WithHidden()` to include them.
- Aggregation waits at most `max(1s, cooldown)` by default, and pending paths are limited to 4096. Capacity pressure can deliver a pending event early.
- Watched roots and event paths use canonical absolute paths, including resolved root symlinks. Update path comparisons or regex filters that depended on an alias.
- Native read buffers must be between 4 KiB and 1 MiB; Windows network watches cap them at 64 KiB.
- Merged raw events are counted by `EventsCoalesced`; `EventsFiltered` counts events excluded by filters.

Applications that require hidden paths and the previous unbounded debounce wait can explicitly configure both policies:

```go
w, err := fswatcher.New(
    fswatcher.WithHidden(),
    fswatcher.WithMaxWait(0),
)
```

System and temporary ignore patterns remain active with `WithHidden()`. Root symlinks now resolve to canonical absolute paths, which are used in `Paths()` and event paths; `DropPath()` also accepts the original configured alias after it has been removed.

## Methods

Once you have a `Watcher` instance from `New()`, you can use the following methods to control it:

| Method | Description |
| :--- | :--- |
| `Watch(ctx)` | Starts the watcher. Blocking — runs until the context is canceled. Run in a goroutine. A watcher instance is single-use: after `Watch()` returns, create a new watcher with `New()` |
| `Events()` | Returns a read-only channel (`<-chan WatchEvent`) for receiving file system events |
| `AddPath(path, options...)` | Adds a new directory to monitor at runtime. Accepts the same `PathOption` values as `WithPath`. Safe to call concurrently with `DropPath()` |
| `DropPath(path)` | Stops monitoring a directory at runtime. Accepts the original alias even after that alias is removed. Safe to call concurrently with `AddPath()` |
| `Close()` | Initiates a graceful shutdown — alternative to canceling the context |
| `IsRunning()` | Returns `true` if `Watch()` is currently running |
| `Paths()` | Returns the canonical absolute paths of configured roots |
| `Stats()` | Returns runtime statistics, including processed, coalesced, dropped, filtered, and lost events. `EventsPartial` counts inotify registrations skipped at watch limits or failed during overflow recovery |
| `Dropped()` | Returns a read-only channel for events dropped because the main `Events()` channel was full |
| `Log(level, message, args...)` | Writes through the configured logger using a `Severity` level and structured arguments |

`Close()` requests shutdown and returns immediately. Before `Watch()` returns, FSWatcher flushes pending aggregation, joins its delivery workers, closes its own event channels, and leaves custom channels open. Wait for this return before releasing caller-owned resources. If a ready channel is supplied, wait for either readiness or the result of `Watch()` so a startup failure cannot leave the caller waiting indefinitely.

Removing an overlapping root preserves native watches still needed by other roots. On Windows, deleting one watched root releases that root's native watch while the other roots continue to run; `DropPath()` can remove the deleted root from the configured paths.

An `EventOverflow` means some changes were lost. Reconcile your state with the filesystem, including deleted entries. Automatic scans can restore watch coverage and report existing files, but cannot reconstruct every lost change. Native read buffers must be at least 4 KiB; Windows network watches cap buffers at 64 KiB.

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
| **Filtering** | Hidden paths, system file rules, and regex patterns are checked. Excluded paths are dropped |
| **Debouncing** | Events for the same path are merged until the cooldown or maximum wait expires. Reaching the pending-path limit can deliver an event early |
| **User channel** | The final, clean `WatchEvent` is sent to `Events()` |

## Event Aggregation

FSWatcher uses an `EventAggregator` to handle the noise of many OS events. For example, when you save a file, the OS might emit multiple "Edit" and "Chmod" events in rapid succession.

The `EventAggregator` works by:
1.  **Deduplication** — multiple events for the same path within the cooldown window are merged
2.  **Type collection** — the final `WatchEvent` contains all unique `EventType` values (e.g., both `Create` and `Edit` if both occurred)
3.  **Flag collection** — all platform-specific flags are collected and merged
4.  **Deterministic ordering** — aggregated `Types` are sorted by enum order and `Flags` are sorted lexicographically
5.  **Timer-based flushing** — an event is sent after the cooldown window or the `WithMaxWait` deadline, whichever comes first. The default maximum wait is `max(1s, cooldown)`; `WithMaxWait(0)` disables this deadline
6.  **Bounded pending paths** — reaching `WithMaxPendingEvents` delivers a pending event early before accepting a new path. The default limit is 4096 paths

## Project structure

```
.
├── watcher.go                 # Core watcher logic and public API
├── options.go                 # Configuration options (functional pattern)
├── event.go                   # Event definitions and aggregation logic
├── filters.go                 # Path filtering logic
├── root_index.go              # Index of configured roots for event filtering
├── wakeup.go                  # Cancellation signals joined before native cleanup
├── hidden_*.go                # Platform-specific hidden attributes
├── logs.go                    # Logging helpers
├── errors.go                  # Custom error types
├── watcher_darwin.go          # macOS (Pure Go bridge)
├── watcher_darwin_fsevents.go # macOS (FSEvents CGO) implementation
├── watcher_kqueue.go          # kqueue implementation (macOS no-cgo & BSD)
├── watcher_bsd.go             # BSD-specific initialization
├── watcher_linux.go           # Linux platform loader
├── watcher_linux_inotify.go   # Linux (inotify) implementation
├── watcher_linux_fanotify.go  # Linux (fanotify; partial support)
├── watcher_windows.go         # Windows (ReadDirectoryChangesW) implementation
├── watcher_unsupported.go     # Other platforms: builds, Watch returns ErrUnsupportedPlatform
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
	"os"
	"os/signal"

	"github.com/sgtdi/fswatcher/v2"
)

func main() {
	ready := make(chan struct{})
	fsw, err := fswatcher.New(
		fswatcher.WithPath("./"),
		fswatcher.WithReadyChannel(ready),
		fswatcher.WithLogFile("stdout"),
		fswatcher.WithSeverity(fswatcher.SeverityDebug),
	)
	if err != nil {
		log.Fatalf("Failed to create watcher: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- fsw.Watch(ctx) }()
	select {
	case <-ready:
		log.Println("Initial paths are ready")
	case err := <-result:
		if err != nil {
			log.Fatal(err)
		}
		return
	}

	for event := range fsw.Events() {
		fmt.Printf("Received event:\n%s", event.String())
	}
	if err := <-result; err != nil {
		log.Fatal(err)
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

## Testing

Run the standard suite and consistency checks with the Go version required by `go.mod`:

```sh
go test -shuffle=on ./...
go vet ./...
go mod verify
go mod tidy -diff
```

On platforms with race-detector support and CGO enabled:

```sh
go test -race -shuffle=on ./...
```

To exercise the kqueue backend on macOS:

```sh
CGO_ENABLED=0 go test -shuffle=on ./...
```

The [Go CI workflow](.github/workflows/ci-test.yml) runs native tests on Linux, Windows, and both macOS backends, plus privileged fanotify integration and BSD compilation checks. The [BSD runtime workflow](.github/workflows/bsd-runtime.yml) executes native test binaries in FreeBSD, OpenBSD, NetBSD, and DragonFly VMs. [CodeQL](.github/workflows/codeql.yml) provides automated static analysis.

Fanotify integration requires suitable privileges and a filesystem that supports file handles. Setting `FSWATCHER_REQUIRE_FANOTIFY=1` makes an unavailable backend fail the integration test instead of skipping it; CI enables this for its privileged fanotify step.

To scan the library and tests for known vulnerabilities:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest -test ./...
```

## FAQ

**1. Why create another file watcher?**

> FSWatcher was built to provide features like built-in debouncing and powerful filtering out-of-the-box, which often require manual implementation in other libraries. It also uses a modern Go API with functional options and context-based lifecycle management

**2. How does it handle a large number of files?**

> It uses native OS APIs, which are highly efficient and do not rely on polling. This allows it to watch directories with hundreds of thousands of files without significant performance degradation, limited only by available system memory and OS-specific limits on file handles

**3. What happens if the event buffer is full?**

> If `Events()` is full, the incoming aggregated event is sent to `Dropped()`. If both channels are full or unable to receive it, the event is discarded and `EventsLost` is incremented. Delivery never blocks the event processing pipeline

**4. Can I watch files, or only directories?**

> FSWatcher watches directories only. This ensures consistent, predictable behavior across all platforms. Be aware that the Linux `inotify` backend can struggle with very large or deep directory trees
