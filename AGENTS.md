# AGENTS.md

Contributor guide for `ltm` (Linux Time Machine).

## What this repo is

`ltm` records machine history on Linux: process exec/exit, file open/write/rename/unlink, and network bind/connect events. Events flow through a narrow `storage.Event` contract, are written to a local SQLite database, and are queried through CLI commands for timeline, diff, natural-language-style questions, and raw SQL.

Two collector modes exist:

- `demo` — synthetic events for development and CI; no kernel privileges.
- `ebpf` — real syscall tracepoints on Linux; requires root or BPF capabilities.

## Repository layout

```text
cmd/ltm/              binary entrypoint only
internal/
  cli/                commands, global flags, daemon spawn
  daemon/             background service, batching, collector wiring
  collector/          ignore-path filtering, bounded buffering, source fan-in
  ebpf/               BPF C source, embedded object, Linux collector
  storage/            SQLite-backed event store, filters, prune
  agent/              external agent CLI bridge (NL question -> SQL)
  diff/               time-window machine-state diff
  query/              deterministic query templates over stored events
tests/                integration script and small HTTP fixture
docs/                 architecture and security notes
```

Only `cmd/` is public import surface. All implementation packages live under `internal/`.

## Build and test

```bash
go test ./...
go build -o bin/ltm ./cmd/ltm
make integration   # demo daemon end-to-end flow
```

On Linux, after changing `internal/ebpf/collector.bpf.c`:

```bash
make ebpf          # rebuild internal/ebpf/collector_bpfel.o (needs clang)
go build -o bin/ltm ./cmd/ltm
```

CI (`.github/workflows/ci.yml`) runs unit tests on Ubuntu and macOS, the integration script on Ubuntu, and a Linux BPF rebuild plus binary build.

## CLI conventions

Global flags must come **before** the subcommand:

```bash
ltm --db /tmp/ltm.db --pidfile /tmp/ltm.pid start --mode ebpf
ltm --db /tmp/ltm.db status
```

`start` re-execs the binary as `daemon --foreground` with global flags forwarded. Do not break that ordering when changing CLI code.

Default paths:

- DB: `~/.local/share/ltm/ltm.db`
- PID: `~/.local/run/ltm.pid`

## Event contract

All collectors emit `storage.Event` (package `internal/storage`). Keep category/action strings stable; `diff` and `query` depend on them.

Common categories: `process`, `file`, `network`, `memory`, `block`.

Common actions: `exec`, `exit`, `open`, `write`, `read`, `rename`, `unlink`, `bind`, `connect`, `listen`.

When extending the schema, bump `storage.SchemaVersion` only if replay or query behavior changes incompatibly.

## eBPF notes

- BPF source: `internal/ebpf/collector.bpf.c`
- Embedded object: `internal/ebpf/collector_bpfel.o` (checked in; rebuilt in CI and via `make ebpf`)
- Linux loader: `internal/ebpf/real_linux.go` (`//go:build linux`)
- Non-Linux stub: `internal/ebpf/real_stub.go`
- Headers under `internal/ebpf/headers/` are minimal MVP stubs, not full libbpf/vmlinux headers
- x86_64 only for now (`__TARGET_ARCH_x86` in BPF build)
- Hooks span process, file, memory, network, and block tracepoints (see `internal/ebpf/tracepoints_linux.go`)
- BPF-side ignores `/proc`, `/sys`, `/dev` and the daemon's own PID to reduce feedback loops
- Rebuild `collector_bpfel.o` on Linux after BPF changes (`make ebpf`); CI does this automatically

## Storage rules

- Store is a SQLite database (`modernc.org/sqlite`, no CGo); the `events` table is the only source of truth, no in-memory event log.
- `Open()` is the single writer (WAL mode, `SetMaxOpenConns(1)`); `OpenReadOnly()` is for every read path and runs with `PRAGMA query_only=ON` so it can never write, including via `ltm sql`.
- `OpenReadOnly()` errors if the DB file does not exist yet — do not silently create one on a read path.
- `storage.Filter` is the structured query surface (time range, pid/uid/comm/category/action, path/exe LIKE patterns); extend it rather than adding more one-off `EventsByX` methods.
- Add or extend tests in `internal/storage/store_test.go` for `Filter` combinations, read-only write-rejection, and prune behavior.
- `storage.SchemaDoc` is shown to users (`ltm query sql` with no args) and embedded in agent prompts — update it whenever the `events` schema changes.
- `Event.DroppedBefore` counts events lost immediately before a given row (kernel perf-buffer loss and collector channel overflow, attributed in `daemon.flushLoop`). Totals are `SUM(dropped_before)`, so keep it additive — never overwrite the running total.
- `ltm watch` tails via `EventsAfterID`/`LatestEventID` on the read-only store; keep those id-ordered and cheap.

## Agent bridge rules

- `internal/agent` shells out to a configured agent CLI (`claude`, `codex`, `cursor`, `gemini`, `auto`, or a custom command via `--agent` / `LTM_AGENT`) to turn a plain English `ltm query` into SQL.
- Agent-generated SQL must only ever run through `OpenReadOnly` + `RawSQL`; `ExtractSQL` additionally rejects non-SELECT and multi-statement output. Keep both layers.
- Agent failure must never break `ltm query`: warn on stderr and fall back to the deterministic templates in `internal/query`.
- Tests use fake agent shell scripts (see `internal/agent/agent_test.go`); never call a real agent CLI from unit tests.

## Code style

- Match existing package boundaries; do not bypass `storage.Event`.
- Keep changes small and focused.
- Prefer extending existing engines over parallel implementations.
- No file contents in the event store — metadata only.
- Respect ignore-path filtering in `internal/collector` for noisy paths (`/proc`, `/sys`, caches).
- New implementation code belongs under `internal/`, not the repo root.
- Do not include `Co-Authored-By` trailers in commit messages.

## What not to do

- Do not add local `replace` directives in `go.mod` for `github.com/cilium/ebpf`.
- Do not commit secrets, VM credentials, or one-off host paths.
- Do not change default ignore paths without updating `docs/security.md`.
- Do not add unrelated docs files unless the user asks.
- Do not import `internal/` packages from outside this module.

## Manual verification

Demo flow:

```bash
./bin/ltm start --mode demo
./bin/ltm status
./bin/ltm timeline --since 10m
./bin/ltm timeline --since 10m --category network
./bin/ltm watch --interval 1s
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /tmp/ltm-demo.txt?"
./bin/ltm query sql "SELECT category, count(*) FROM events GROUP BY category"
./bin/ltm version
./bin/ltm stop
```

eBPF flow (Linux host):

```bash
sudo ./bin/ltm start --mode ebpf
echo test >> /tmp/ltm-demo.txt
./bin/ltm query "who modified /tmp/ltm-demo.txt?"
sudo ./bin/ltm stop
```

## Related docs

- `README.md` — user-facing overview and examples
- `docs/architecture.md` — pipeline and phase notes
- `docs/security.md` — privileges and data handling
