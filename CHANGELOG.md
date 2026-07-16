# Changelog

All notable changes to `ltm` are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Self-capture feedback loop.** `ltm status`/`query`/`timeline` read the
  SQLite store as separate processes; the recorder captured those reads,
  stored them, and re-read them — a self-amplifying loop that flooded the ring
  buffer and inflated the drop count (a single query produced ~9k events and
  ~15k drops in one second). The recorder now filters its own activity at two
  layers, neither needing any `--ignore-path`:
  - **Kernel:** `should_skip` in `collector.bpf.c` skips any task whose comm is
    `ltm`, shutting the loop at the source before an event is reserved (no ring
    pressure, no drops). Trade-off: a process literally named `ltm` is invisible
    to the recorder (documented in `docs/security.md`).
  - **Userspace:** the daemon auto-adds its `--db` path (plus `-wal`/`-shm`/
    `-journal` sidecars) and `--pidfile` to the collector ignore rules.

### Added

- **Feedback-loop diagnostic in `ltm status`.** When events are being dropped,
  `status` now prints the busiest recent producers and flags a suspected loop
  if `ltm` itself is among them. Included in `--json` output under
  `diagnostics`.

[Unreleased]: https://github.com/Agent-Hellboy/ltm/compare/v0.4.1...HEAD
