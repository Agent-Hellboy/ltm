# AGENTS.md

Contributor guide for `ltm` (Linux Time Machine).

## What this repo is

`ltm` records machine history on Linux: process exec/exit, file open/write/rename/unlink, and network bind/connect events. Events flow through a narrow `storage.Event` contract, are appended to a local JSONL store, and are queried through CLI commands for timeline, diff, and natural-language-style questions.

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
  storage/            append-only store, indexes, replay on open
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
ltm --db /tmp/ltm.log --pidfile /tmp/ltm.pid start --mode ebpf
ltm --db /tmp/ltm.log status
```

`start` re-execs the binary as `daemon --foreground` with global flags forwarded. Do not break that ordering when changing CLI code.

Default paths:

- DB: `~/.local/share/ltm/ltm.log`
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

- Store is append-only JSONL.
- `Open()` replays the file into in-memory indexes (`load()` → `applyEventLocked()`).
- Any fix to replay must keep timeline, diff, and query consistent after restart.
- Add or extend tests in `internal/storage/store_test.go` for replay and query regressions.

## Code style

- Match existing package boundaries; do not bypass `storage.Event`.
- Keep changes small and focused.
- Prefer extending existing engines over parallel implementations.
- No file contents in the event store — metadata only.
- Respect ignore-path filtering in `internal/collector` for noisy paths (`/proc`, `/sys`, caches).
- New implementation code belongs under `internal/`, not the repo root.

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
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /tmp/ltm-demo.txt?"
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
