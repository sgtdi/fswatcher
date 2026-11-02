# Changelog

Notable changes are recorded here starting with version 2.0.0. Earlier release notes are available in [GitHub Releases](https://github.com/sgtdi/fswatcher/releases).

## [Unreleased]

### Added

- Build on platforms without a native backend, such as js/wasm, wasip1 and plan9, where `Watch` returns the new `ErrUnsupportedPlatform`, which matches `errors.ErrUnsupported`. Programs that run on those platforms no longer need build tags to exclude the package.

### Changed

- Require Go 1.27.0 or later, the minimum version that the module needs, instead of 1.27.3, so that applications are not required to use a newer Go than they do.

### Fixed

- Use `kqueue` on iOS, where FSEvents is not available; building for iOS with CGO enabled failed.

## [2.0.0] - 2026-11-02

### Breaking changes

- Adopt the Go module path `github.com/sgtdi/fswatcher/v2`. Applications upgrading from v1 must update their imports.
- Require Go 1.27.3 or later.
- Exclude hidden paths by default: dot-prefixed names on Unix-like systems, `FILE_ATTRIBUTE_HIDDEN` on Windows, and `UF_HIDDEN` on macOS and FreeBSD. Use `WithHidden()` to include them; system and temporary ignore patterns still apply.
- Bound aggregation by default with a maximum wait of `max(1s, cooldown)` and 4096 pending paths. Continuous changes are delivered by the maximum wait, and capacity pressure can deliver pending events early. `WithMaxWait(0)` disables the time cap; `WithMaxPendingEvents` configures the pending-path limit.
- Resolve watched roots to canonical absolute paths, including symlinks, and use those paths in events and `Paths()`. `DropPath()` also accepts the original configured alias after the alias has been removed.
- Reject native read buffers smaller than 4 KiB. Windows network watches cap the buffer at 64 KiB.
- Count coalesced raw events separately in `EventsCoalesced`; `EventsFiltered` now counts events excluded by filters.

### Added

- `WithHidden()`, `WithMaxWait()`, and `WithMaxPendingEvents()` configuration options.
- `WithIgnoreList()` and `DefaultIgnores()` for customizing platform-specific system and temporary file patterns.
- Runtime tests in native FreeBSD, OpenBSD, NetBSD, and DragonFly VMs, plus privileged fanotify integration in CI.
- Regression coverage for cancellation and shutdown, hidden attributes, overlapping roots, symlink aliases, root events, directory moves, overflow recovery, and bounded aggregation.

### Fixed

- Give v2 FSEvents callbacks distinct native symbols so applications can use v1 and v2 together with CGO enabled on macOS.
- Synchronize event delivery, scan workers, cancellation signals, and runtime path changes with native cleanup to avoid access to released resources.
- Preserve watch coverage shared by overlapping roots when one root is removed.
- Track moved-in directory trees and enforce top-level depth in kqueue; remove stale descriptors after directory renames.
- Refresh native hidden attributes on metadata changes and restore kqueue coverage when initially hidden or newly added files and directories become visible.
- Reconcile inotify directory moves by cookie, expire watches that leave the watched tree, and rebuild registrations after queue overflow.
- Resolve every fanotify file handle independently, maintain recursive directory marks, and preserve marks shared by overlapping roots.
- Rearm recoverable Windows notification errors and retire deleted roots while keeping other roots active.
- Report unexpected backend shutdowns and native overflow instead of leaving a silently inactive watcher.
- Allow removal of configured paths after their directory or alias has been deleted.

### Performance

- Index configured roots by path components for event filtering.
- Use a shared aggregation worker with a deadline heap and bounded pending state.
- Cache native hidden-attribute lookups with invalidation on relevant events.
- Wake kqueue directly on cancellation and yield cooperatively during large directory scans.

### Maintenance

- Update `golang.org/x/sys` to v0.49.0, Testify to v1.12.1, and the YAML test dependency to v3.0.6.
- Verify test compilation across supported BSD targets, including 32-bit architectures, and use virtual time for aggregation timing tests.
- Document all public options and methods, migration, channel ownership, backpressure, overflow recovery, platform limitations, and validation commands.

### Migration

Install the new module and change imports to `github.com/sgtdi/fswatcher/v2`:

```sh
go get github.com/sgtdi/fswatcher/v2@v2.0.0
```

Applications that require hidden entries and an unbounded debounce wait can configure `WithHidden()` and `WithMaxWait(0)`. See the [migration guide](README.md#updating-existing-callers) for the complete behavior changes.

[2.0.0]: https://github.com/sgtdi/fswatcher/compare/v1.3.0...v2.0.0
