# fswatcher Codebase Audit & Improvement Roadmap

> **Last audited**: 2026-03-11
> **Auditor**: Claude Opus 4.6 + manual code review
> **Scope**: Every `.go` source file in the repository root

This document tracks all identified fragile points, bugs, planned improvements, and performance optimisations. Each item includes the exact location, a detailed explanation, current status, and a commit message template.

---

## Table of Contents

1. [Fragile Points (Bugs & Stability)](#-fragile-points-bugs--stability-issues)
2. [Recommended Improvements](#-recommended-improvements)
3. [Performance Optimisations](#-performance-optimisations)
4. [Dead Code & Unused Declarations](#-dead-code--unused-declarations)

---

## 🛡️ Fragile Points (Bugs & Stability Issues)

### Resolved

- [x] **FP-1 — Critical: Debouncer Information Loss**
    - **Description**: The old `debouncer.go` used a `ShouldProcess()` method that returned `false` on merged events, causing all event types after the first to be silently discarded. For example, a `Create` followed by a `Mod` on the same path within the cooldown window would only deliver the `Create`.
    - **Root Cause**: A boolean gate prevented re-emission of events within the cooldown, but it discarded the *types* of merged events rather than accumulating them.
    - **How it was fixed**: `debouncer.go` was deleted entirely. A new `EventAggregator` was added to `event.go`. It stores event types in a `map[EventType]struct{}`, guaranteeing every type is preserved across merges. A per-path `time.AfterFunc` timer replaces the old cooldown gate, so the aggregated event is flushed only after the cooldown expires.
    - **Verification**: `event.go:84–139` — `addEvent()` merges into the map; `event.go:141–172` — `flushPath()` collects all unique types into the final `WatchEvent`.
    - **Impact**: High (Data Loss) — FIXED
    - **File**: `debouncer.go` → `event.go`

- [x] **FP-2 — Kqueue File Descriptor Exhaustion**
    - **Description**: The kqueue backend (used on macOS without CGO and all BSDs) opens a file descriptor for *every* file and directory via `unix.Open()` + `unix.Kevent()`. On trees with more files than the per-process FD limit (`ulimit -n`), this would crash with `EMFILE`/`ENFILE`.
    - **How it was fixed**: Both `addSingleWatchLocked` (`watcher_kqueue.go:316–319`) and `batchAddWatchesLocked` (`watcher_kqueue.go:241–243`) now check for `unix.EMFILE` and `unix.ENFILE` after `unix.Open()` fails. Instead of crashing, they emit a structured `logWarn` advising the user to increase system limits and skip the unreadable file. The batch path (`batchAddWatchesLocked`) additionally continues to the next entry rather than aborting the whole batch.
    - **Limitation**: The fundamental architecture still requires one FD per watched item. The advisory is a mitigation, not a cure — see **IMP-6** below for the full fix.
    - **Impact**: Medium/High (Crash on large directories) — MITIGATED
    - **Files**: `watcher_kqueue.go:300–322`, `watcher_kqueue.go:234–245`

- [x] **FP-3 — Windows Buffer Alignment**
    - **Description**: The `ReadDirectoryChangesW` Win32 API requires the result buffer to be aligned to a 64-bit (8-byte) boundary. A plain `make([]byte, ...)` in Go does not guarantee this alignment.
    - **How it was fixed**: The buffer is now allocated as `make([]uint64, (w.bufferSize+7)/8)`, which the Go runtime guarantees to be 8-byte aligned. It is then reinterpreted as a byte slice via `unsafe.Slice` for the syscall.
    - **Verification**: `watcher_windows.go:229–234`.
    - **Impact**: Medium (Potential crash / undefined behaviour on Windows) — FIXED
    - **File**: `watcher_windows.go`

- [x] **FP-4 — Fanotify Path Resolution Fragility**
    - **Description**: The fanotify backend reconstructs file paths from opaque kernel file handles via `OpenByHandleAt` → `/proc/self/fd/<fd>` → `os.Readlink`. This is slow and can fail if the dentry is evicted from the kernel cache.
    - **How it was fixed**: A `mountCache map[uint64]string` field was added to the `fanotify` struct (`watcher_linux_fanotify.go:23`). After a successful `OpenByHandleAt` resolution, the FSID → directory mapping is cached. Subsequent events on the same filesystem skip the syscall and use the cached path directly. Cache reads use `RLock` for concurrency; writes use `Lock`.
    - **Verification**: `watcher_linux_fanotify.go:170–176` (read), `watcher_linux_fanotify.go:194–198` (write).
    - **Limitation**: The cache is never invalidated or bounded. If the watched filesystem is unmounted and remounted at a different path, stale entries could cause incorrect paths. In practice this is rare for a file watcher.
    - **Impact**: Medium (Missing events on Linux) — FIXED
    - **File**: `watcher_linux_fanotify.go`

- [x] **FP-5 — Platform Loop Error Recovery**
    - **Description**: All four platform event loops (inotify, kqueue, fanotify, Windows) had `continue` on syscall errors with no delay, which could spin the CPU at 100% on persistent failures.
    - **How it was fixed**: A shared `backoffState` struct (`watcher.go:568–571`) and two methods were added:
        - `handleLoopError` (`watcher.go:581–592`): increments a retry counter, logs the error, sleeps for the current backoff duration, and doubles it. Returns `false` (shut down the loop) after 5 consecutive failures.
        - `resetBackoff` (`watcher.go:595–598`): resets to the initial 10 ms delay on success.
    - **Verification**: Each loop creates `backoff := newBackoffState()` and calls `handleLoopError` / `resetBackoff` — confirmed in `watcher_kqueue.go:83,100,107`, `watcher_windows.go:103,122,129`, `watcher_linux_inotify.go:179,188,195`, `watcher_linux_fanotify.go:69,88,95`.
    - **Impact**: Low/Medium (Resource exhaustion) — FIXED
    - **Files**: `watcher.go`, all four platform files

### Open

- [x] **FP-6 — Events Proxy Goroutine Overhead**
    - **Description**: When the user configured `SeverityInfo` or `SeverityDebug`, `Events()` created a second channel (`eventsProxy`) and a goroutine via `sync.Once` that read from `w.events`, logged, and re-forwarded. This doubled channel hops, memory (two full-sized buffers), and added a goroutine.
    - **How it was fixed**:
        - `proxy sync.Once` and `eventsProxy chan WatchEvent` fields removed from the `watcher` struct.
        - `Events()` simplified to `return w.events` — one line, no goroutine, no allocation.
        - `w.logDebug("Raw event: %s", event.String())` moved to the top of `sendToChannel()`, before the channel send. `logDebug` is already gated by slog's internal level filter so it is a no-op at `SeverityWarn`/`SeverityError` with zero overhead.
    - **Side effect**: Buffer capacity is now consistently `bufferSize` regardless of log level. Previously at Info/Debug, effective capacity was `2 × bufferSize` (events could buffer in both channels).
    - **Impact**: Low (Performance) — FIXED
    - **File**: `watcher.go`

- [ ] **FP-7 — `uniqueEventTypes` Silently Drops `EventOverflow`**
    - **Description**: The `uniqueEventTypes` function (`event.go:198–217`) uses a fixed-size boolean array for deduplication: `const maxEventType = EventChmod + 1` which equals `6`. But `EventOverflow` is also `6` (it's the next iota after `EventChmod`). The guard `v < maxEventType` evaluates to `6 < 6 = false`, so any `EventOverflow` element is silently discarded. The inline comment on line 203 ("The max value is EventChmod (5)") is also stale.
    - **Current risk**: No platform mapping function currently produces `EventOverflow` — it's only injected directly by `watcher_windows.go:146`. That code path does not go through `uniqueEventTypes`, so the bug is dormant. However, it will bite anyone who adds `EventOverflow` to a mapping function in the future.
    - **Proposed fix**: Change the constant to `const maxEventType = EventOverflow + 1` and update the comment.
    - **Impact**: Low now; Medium if `EventOverflow` is ever routed through `uniqueEventTypes`
    - **File**: `event.go:203–204`
    - **Commit Message**: `fix: include EventOverflow in uniqueEventTypes bounds check`

- [ ] **FP-8 — Path Prefix Matching False Positive in `handlePlatformEvent`**
    - **Description**: `watcher.go:612` uses `strings.HasPrefix(filepath.Clean(event.Path), filepath.Clean(watchedDir))` to find the parent watch for an event. This has a false-positive problem: watching `/tmp/foo` will match events from `/tmp/foobar` because `HasPrefix("/tmp/foobar", "/tmp/foo")` is `true`. The same pattern appears in `scanDirectory` at `watcher.go:545`.
    - **Existing solution**: `watcher_linux_inotify.go:422–425` already defines `isSubpath()` which uses `filepath.Rel` and properly validates path boundaries. This function should be promoted to a shared utility and used in both `handlePlatformEvent` and `scanDirectory`.
    - **Proposed fix**: Move `isSubpath` to `filters.go` or `watcher.go`. Replace both `strings.HasPrefix` checks. Additionally, the match should check for exact equality (`event.Path == watchedDir`) OR `isSubpath(watchedDir, event.Path)`.
    - **Impact**: Low (Incorrect event attribution for paths sharing a prefix)
    - **Files**: `watcher.go:612`, `watcher.go:545`
    - **Commit Message**: `fix: use filepath.Rel-based subpath check to prevent false prefix matches`

- [ ] **FP-9 — FSEvents C Debug Flag Direction Inverted**
    - **Description**: In `watcher_darwin_fsevents.go:348`, the condition `f.w.severity >= SeverityDebug` enables the C debug flag. Since `SeverityDebug = -4` and all higher severities (Info=0, Warn=4, Error=8) are `>= -4`, the C debug mode is active for *every* non-`SeverityNone` severity level. This is inconsistent with the Go-side convention: `watcher.go:231` uses `w.severity <= SeverityDebug` to guard debug-only blocks.
    - **Effect**: When a user configures `SeverityWarn` (the default), the C code's `fprintf(stderr, ...)` error paths are still guarded by `if (debug)` and will print to stderr on error. This is unexpected because the user only asked for warnings via the Go logger, not raw C stderr output.
    - **Proposed fix**: Change `f.w.severity >= SeverityDebug` to `f.w.severity <= SeverityDebug` to match the Go-side convention.
    - **Impact**: Low (Unexpected stderr output)
    - **File**: `watcher_darwin_fsevents.go:348`
    - **Commit Message**: `fix(darwin): correct severity comparison direction for C debug flag`

- [ ] **FP-10 — Incomplete `log/slog` Transition — Residual `log.Printf`**
    - **Description**: `watcher.go:7` still imports the `"log"` package. It is used exactly once, at `watcher.go:299`: `log.Printf("fswatcher: error closing log file: %v", err)`. Every other log call in the codebase uses the `slog`-based helpers in `logs.go`.
    - **Context**: This happens during shutdown after the slog logger's backing file has already been closed. Using the slog logger here would write to a closed file. The correct fix is `fmt.Fprintf(os.Stderr, ...)` which avoids both the `"log"` import and the closed-file issue.
    - **Impact**: Cosmetic (Inconsistent logging, unnecessary import)
    - **File**: `watcher.go:7`, `watcher.go:299`
    - **Commit Message**: `fix: replace residual log.Printf with fmt.Fprintf(os.Stderr, ...)`

- [ ] **FP-11 — inotify Queue Overflow Not Propagated to Consumer**
    - **Description**: `watcher_linux_inotify.go:246–249` detects `IN_Q_OVERFLOW` and logs an error, but immediately returns without emitting an event. Consumers have no way to know events were lost. This is inconsistent with the Windows backend (`watcher_windows.go:144–150`) which emits `WatchEvent{Types: []EventType{EventOverflow}}` on buffer overflow.
    - **Proposed fix**: After logging, call `w.handlePlatformEvent(WatchEvent{Path: "", Types: []EventType{EventOverflow}, Time: now})` so consumers can detect the overflow. This also motivates fixing FP-7 (the `uniqueEventTypes` bound) since the Overflow event would now flow through the aggregator.
    - **Impact**: Medium (Silent data loss on inotify; cross-platform API inconsistency)
    - **File**: `watcher_linux_inotify.go:246–249`
    - **Commit Message**: `feat(linux): emit EventOverflow on inotify queue overflow`

- [ ] **FP-12 — `WatchPath.eventMask` Is Never Checked**
    - **Description**: The `WithEventMask` option (`options.go:128–137`) populates `WatchPath.eventMask`, and this field is preserved through `AddPath` (`watcher.go:325`) and `WithPath` (`options.go:95`). However, no code anywhere in the event processing pipeline actually reads `eventMask` to filter events. Events of all types are always delivered regardless of the mask.
    - **Current state**: The option exists, tests confirm it's stored (`options_test.go:163–166`), but it has no runtime effect. Users relying on it would receive unfiltered events.
    - **Proposed fix**: In `handlePlatformEvent`, after finding `parentWatch`, check if `parentWatch.eventMask` is non-nil and non-empty. If so, filter `event.Types` to only include types present in the mask. If no types remain after filtering, increment `eventsFiltered` and return.
    - **Impact**: Medium (Public API option has no effect — contract violation)
    - **File**: `watcher.go:600–649`, `options.go:128–137`
    - **Commit Message**: `fix: enforce WatchPath.eventMask filtering in handlePlatformEvent`

- [ ] **FP-13 — `WatchPath.filter` (Per-Path Filter) Is Never Used**
    - **Description**: The `WatchPath` struct has a `filter PathFilter` field (`options.go:113`), and it's preserved through `AddPath` (`watcher.go:324`) and `WithPath` (`options.go:94`). However, `handlePlatformEvent` (`watcher.go:641`) only checks `w.filter` (the global watcher-level filter), not the per-path filter. There is no `PathOption` to set it either, so it's currently always `nil` — but the field and preservation code suggest it was designed for per-path filtering.
    - **Impact**: Low (Dead field, no public setter exists yet — but the plumbing is misleadingly present)
    - **File**: `options.go:113`, `watcher.go:324`
    - **Commit Message**: `fix: either implement per-path filter checking or remove the dead field`

- [ ] **FP-14 — `eventWg` WaitGroup Is Never Incremented**
    - **Description**: `watcher.go:94` declares `eventWg sync.WaitGroup`, and `watcher.go:295` calls `w.eventWg.Wait()` during shutdown. However, no code calls `w.eventWg.Add()` or `w.eventWg.Done()` anywhere. The `Wait()` returns immediately and serves no purpose.
    - **Context**: This was likely intended to wait for in-flight event processing goroutines to complete during shutdown. Currently there are no such goroutines (events are sent synchronously via `sendToChannel`), so the WaitGroup is dead.
    - **Impact**: Cosmetic (Dead synchronisation primitive, misleading)
    - **File**: `watcher.go:94`, `watcher.go:295`
    - **Commit Message**: `fix: remove unused eventWg WaitGroup or implement proper goroutine tracking`

---

## 🚀 Recommended Improvements

### Resolved

- [x] **IMP-1 — Unify Aggregation (Debouncer + Batcher)**
    - **Goal**: Merge the old `debouncer` and `eventBatcher` into a single `EventAggregator`.
    - **How it was done**: `debouncer.go` was deleted. `EventAggregator` in `event.go:56–195` handles both per-path deduplication (via `map[string]*aggregatedEvent`) and cooldown-based flushing (via `time.AfterFunc` per path). The aggregator merges event types, flags, and IDs.
    - **Verification**: `watcher.go:203` creates it; `watcher.go:648` feeds events to it; `watcher.go:287–289` closes it during shutdown.

- [x] **IMP-2 — Handle Windows Buffer Overflow**
    - **Goal**: Detect `ReadDirectoryChangesW` buffer overflows and notify the consumer.
    - **How it was done**: In `watcher_windows.go:141–151`, when `bytesRead == 0` with no error (the documented overflow signal), the watcher logs a warning and emits `WatchEvent{Types: []EventType{EventOverflow}}` via `handlePlatformEvent`.

- [x] **IMP-3 — FSEvents `MustScanSubDirs` Support**
    - **Goal**: When macOS FSEvents signals `MustScanSubDirs` (indicating the kernel dropped events and the full directory state is uncertain), perform a manual recursive scan.
    - **How it was done**: In `watcher_darwin_fsevents.go:241–244`, when the flags include `MustScanSubDirs`, `UserDropped`, or `KernelDropped`, a goroutine is launched to call `w.scanDirectory(path)`. This method (`watcher.go:515–565`) walks the directory tree and emits `EventMod` for every file/directory found, ensuring the consumer's state is brought up to date.

- [x] **IMP-4 — Transition to `log/slog`**
    - **Goal**: Replace the standard `log` package with structured `slog` for typed, leveled logging.
    - **How it was done**: `logs.go` defines `Severity` constants mapped to `slog.Level` values (`SeverityNone=-8`, `SeverityDebug=-4`, `SeverityInfo=0`, `SeverityWarn=4`, `SeverityError=8`). Helper methods `logError`, `logWarn`, `logInfo`, `logDebug`, and the generic `log` all delegate to `w.logger` (an `*slog.Logger`). The logger is created in `initLogger()` with a `slog.TextHandler` writing to either stdout or a file.
    - **Remaining gap**: One `log.Printf` call at `watcher.go:299` — tracked as FP-10 above.

- [x] **IMP-5 — Lazy Logger Initialisation**
    - **Goal**: Defer log file opening from `New()` to `Watch()` so that file-open errors are returned from the blocking call, not the constructor.
    - **How it was done**: `New()` (`watcher.go:133–206`) stores `logPath` and `severity` but does not open files. `Watch()` (`watcher.go:223`) calls `w.initLogger()` as its first step. `initLogger()` (`watcher.go:465–493`) opens the file and creates the `slog.Logger`.

### Open

- [ ] **IMP-6 — Improve Kqueue Efficiency**
    - **Goal**: Watch only directories with kqueue and manually scan for file changes, drastically reducing FD usage.
    - **Current state**: kqueue opens one FD per file AND directory. `addSingleWatchLocked` (`watcher_kqueue.go:300–346`) calls `unix.Open` on every item. `addWatch` (`watcher_kqueue.go:287–297`) walks the entire directory tree.
    - **Complexity**: kqueue's `NOTE_WRITE` on a directory fires when the directory listing changes (file created/deleted/renamed), but not when a file's *content* changes. To detect content changes with directory-only watching, you'd need to snapshot directory listings and diff them on each event, or switch to polling file mtimes.
    - **Alternative approach**: Keep file FDs only for files in the top-level of each watched directory, and rely on directory `NOTE_WRITE` to detect new files in subdirectories (which already happens via `scanDirectoryLocked`).
    - **Impact**: High (Scalability on BSD / macOS without CGO)
    - **File**: `watcher_kqueue.go`
    - **Commit Message**: `perf: implement directory-only watching for kqueue backend`

- [x] **IMP-7 — Fix `watcher_kqueue.go` Indentation**
    - **Goal**: The bodies of `runKqueueLoop`, `scanDirectoryLocked`, and `addSingleWatchLocked` had inconsistent indentation breaking `gofmt` conventions.
    - **Details fixed**:
        - `runKqueueLoop`: entire loop body after `defer` was indented one extra tab; closing `}}` on a single line split into separate `}` lines.
        - `scanDirectoryLocked`: inner `if !exists` block and `if len(newEntries) > 0` used mixed spaces+tabs; reformatted to standard single-tab indentation.
        - `addSingleWatchLocked`: inner `if err != nil` after the `O_RDONLY` fallback had 5-tab indentation; corrected to 2 tabs.
    - **Impact**: Cosmetic — FIXED
    - **File**: `watcher_kqueue.go`

- [ ] **IMP-8 — Promote `isSubpath` to a Shared Utility**
    - **Goal**: The `isSubpath` and `startsWithDotDot` helpers in `watcher_linux_inotify.go:421–430` are only available on Linux. They should be moved to `filters.go` (which has no build tags) so they can be used in `handlePlatformEvent` and `scanDirectory` on all platforms. This is a prerequisite for fixing FP-8.
    - **Impact**: Low (enables FP-8 fix)
    - **File**: `watcher_linux_inotify.go:421–430` → `filters.go`
    - **Commit Message**: `refactor: move isSubpath to shared filters.go`

---

## ⚡ Performance Optimisations

- [ ] **PERF-1 — Remove Proxy Goroutine in `Events()`**
    - Same as FP-6 above. Merging the log call into `sendToChannel` eliminates one goroutine, one channel allocation, and one channel hop per event.

- [ ] **PERF-2 — Kqueue Directory-Only Watching**
    - Same as IMP-6 above. Reduces FD count from O(files) to O(directories).

- [ ] **PERF-3 — Fanotify Mount Cache Eviction**
    - **Description**: `fanotify.mountCache` (`watcher_linux_fanotify.go:23`) grows unboundedly. On systems where many different filesystems generate events, this map will accumulate entries forever.
    - **Proposed fix**: Add a size cap (e.g. 256 entries) and use a simple LRU or periodic cleanup. Alternatively, since the cache maps FSID → directory path and FSIDs are stable per-mount, the entries are valid for the lifetime of the mount — so a bounded map with eviction on least-recently-used would be appropriate.
    - **Impact**: Low (Memory in long-running processes with many distinct mounts)
    - **File**: `watcher_linux_fanotify.go:23`
    - **Commit Message**: `perf(linux): add LRU eviction to fanotify mount cache`

---

## 🗑️ Dead Code & Unused Declarations

- [x] **DC-1 — Unused Constants: `DefaultCleanupInterval`, `CleanupAge`, `CleanupThreshold`**
    - **Location**: `watcher.go` (removed)
    - **Description**: These three constants were declared but never referenced anywhere in the codebase. Remnants of a planned or removed periodic cleanup feature for the old debouncer/batcher.
    - **How it was fixed**: Removed from the `DefaultCleanupInterval` / `CleanupAge` / `CleanupThreshold` declarations in the `const` block. `DefaultCooldown` and `DefaultBufferSize` were kept.

- [x] **DC-2 — Unused Constant: `MinCooldownDuration`**
    - **Location**: `watcher.go` (removed)
    - **Description**: Declared but never used. `validateConfig()` checks `w.cooldown < 0` but never enforced the minimum via this constant.
    - **How it was fixed**: Removed from the limits `const` block. `MaxCooldownDuration`, `MaxBufferSize`, `MinDroppedBuffer`, and `MaxDroppedBufferRatio` were kept.

- [x] **DC-3 — Unused Field: `watcher.path`**
    - **Location**: `watcher.go` (removed)
    - **Description**: The struct field `path string` was declared but never assigned, so it was always `""`. It appeared once in `newError("start", w.path, ...)` which silently produced a path-less error.
    - **How it was fixed**: Field removed from the `watcher` struct. The one usage was updated to `newError("start", "", ...)` which is semantically identical but explicit.

- [ ] **DC-4 — Unused `eventWg` WaitGroup**
    - Same as FP-14 above. The `Add`/`Done` calls are missing, so the `Wait` is a no-op.
