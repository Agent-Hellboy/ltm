# AGENTS.md

Contributor guide for `ltm` (Linux Time Machine).

## What this repo is

`ltm` records machine history on Linux: process exec/exit, file open/write/rename/unlink, and network bind/connect events. Events flow through a narrow `storage.Event` contract, are appended to a local JSONL store, and are queried through CLI commands for timeline, diff, and natural-language-style questions.

Two collector modes exist:

- `demo` — synthetic events for development and CI; no kernel privileges.
- `ebpf` — real syscall tracepoints on Linux; requires root or BPF capabilities.

## Repository map

| Path | Role |
|------|------|
| `cmd/ltm/` | Binary entrypoint |
| `cli/` | Commands, global flags, daemon spawn |
| `daemon/` | Background service, batching, collector wiring |
| `collector/` | Ignore-path filtering, bounded buffering, source fan-in |
| `ebpf/` | BPF program (`collector.bpf.c`), embedded object, Linux collector |
| `storage/` | Append-only store, indexes, replay on open |
| `diff/` | Time-window machine-state diff |
| `query/` | Deterministic query templates over stored events |
| `tests/` | Integration script and small HTTP fixture |
| `docs/` | Architecture and security notes |

## Build and test

```bash
go test ./...
go build -o bin/ltm ./cmd/ltm
make integration   # demo daemon end-to-end flow
```

On Linux, after changing `ebpf/collector.bpf.c`:

```bash
make ebpf          # rebuild ebpf/collector_bpfel.o (needs clang)
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

All collectors emit `storage.Event`. Keep category/action strings stable; `diff` and `query` depend on them.

Common categories: `process`, `file`, `network`.

Common actions: `exec`, `exit`, `open`, `write`, `rename`, `unlink`, `bind`, `connect`, `listen`.

When extending the schema, bump `storage.SchemaVersion` only if replay or query behavior changes incompatibly.

## eBPF notes

- BPF source: `ebpf/collector.bpf.c`
- Embedded object: `ebpf/collector_bpfel.o` (checked in; rebuilt in CI and via `make ebpf`)
- Linux loader: `ebpf/real_linux.go` (`//go:build linux`)
- Non-Linux stub: `ebpf/real_stub.go`
- Headers under `ebpf/headers/` are minimal MVP stubs, not full libbpf/vmlinux headers
- x86_64 only for now (`__TARGET_ARCH_x86` in BPF build)
- Hooks span process, file, memory, network, and block tracepoints (see `ebpf/tracepoints_linux.go`)
- BPF-side ignores `/proc`, `/sys`, `/dev` and the daemon's own PID to reduce feedback loops
- Rebuild `collector_bpfel.o` on Linux after BPF changes (`make ebpf`); CI does this automatically

After BPF changes: rebuild the object, verify Linux build, test on a real Linux host with `sudo ltm start --mode ebpf`.

## Storage rules

- Store is append-only JSONL.
- `Open()` replays the file into in-memory indexes (`load()` → `applyEventLocked()`).
- Any fix to replay must keep timeline, diff, and query consistent after restart.
- Add or extend tests in `storage/store_test.go` for replay and query regressions.

## Code style

- Match existing package boundaries; do not bypass `storage.Event`.
- Keep changes small and focused.
- Prefer extending existing engines over parallel implementations.
- No file contents in the event store — metadata only.
- Respect ignore-path filtering in `collector/` for noisy paths (`/proc`, `/sys`, caches).

## What not to do

- Do not add local `replace` directives in `go.mod` for `github.com/cilium/ebpf`.
- Do not commit secrets, VM credentials, or one-off host paths.
- Do not change default ignore paths without updating `docs/security.md`.
- Do not add unrelated docs files unless the user asks.

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
