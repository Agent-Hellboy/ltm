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

- **Discrete kernel fault events + disk-latency correlation (observability
  Phases 2–3).** New eBPF programs emit immediate events for hard faults, with
  detail in the event `metadata` JSON (all `optional`, kernel-config dependent):
  - `oom/mark_victim` → `memory`/`oom_kill` (victim pid/comm/uid, `rss_bytes`).
  - `sched/sched_process_hang` → `process`/`hang` (hung-task detector).
  - `block/block_rq_error` → `block`/`error` (`errno`, `sector`, `rwbs`).
  - Disk latency: `block_rq_issue` timestamps each request in a BPF map;
    `block_rq_complete` computes service latency and emits `block`/`slow_io`
    only above 100 ms (`latency_ns` in metadata), so volume stays bounded.
- **Resource sampling timeline (observability Phase 1).** A userspace sampler
  reads `/proc` + PSI and records two new tables, queryable via `ltm query sql`:
  - `system_samples` (~1s): CPU %, load, runnable/blocked procs, memory/swap,
    CPU/memory/I/O pressure (PSI avg10), aggregate disk and network throughput.
  - `process_samples` (~5s): per-process CPU %, RSS, state, thread count,
    cumulative I/O, and cgroup — for every process.
  Cadence is configurable via `daemon.Config` (`SystemSampleEvery` /
  `ProcessSampleEvery`; a negative value disables that sampler). `Prune` now
  trims these tables alongside `events`. Sampling is Linux-only (no-op stub
  elsewhere) and needs no eBPF. Schema codegen (`abi.yaml` → generated DDL +
  `SchemaDoc`) now supports multiple tables.
- **System resource line in `ltm status`.** Shows the latest sample
  (cpu/load/mem/swap/PSI); included in `--json` under `system`.
- **Feedback-loop diagnostic in `ltm status`.** When events are being dropped,
  `status` now prints the busiest recent producers and flags a suspected loop
  if `ltm` itself is among them. Included in `--json` output under
  `diagnostics`.

[Unreleased]: https://github.com/Agent-Hellboy/ltm/compare/v0.4.1...HEAD
