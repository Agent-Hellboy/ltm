# AGENTS.md

Contributor guide for `ltm`. User overview: `README.md`. Detailed docs:
`docs/` (index, ABI, CLI, querying, recording, architecture, security).

## Layout

```text
cmd/ltm/           entrypoint only (public import surface)
internal/
  abi/             handwritten abi.yaml + generated schema/tracepoint/kernel contract
  cli/             commands, global flags, daemon spawn
  daemon/          batching, flushLoop, collector wiring
  collector/       ignore-path filter, bounded buffer
  ebpf/            BPF C, embedded .o, Linux collector + stub
  storage/         SQLite store, Filter, prune
  agent/           NL question → SQL (external CLI)
  diff/            time-window machine-state diff
  query/           deterministic NL templates
tests/             integration.sh (Linux + root)
docs/              README index + abi, cli, querying, recording, architecture, security
```

All implementation under `internal/`. Do not add packages at the repo root.

## Build

```bash
go test ./...
go build -o bin/ltm ./cmd/ltm
make generate             # regenerate ABI Go/C outputs from abi.yaml
make integration          # eBPF smoke; Linux + root
make ebpf                 # after editing collector.bpf.c or abi.yaml (clang, Linux)
```

CI: unit tests (Ubuntu), integration (Ubuntu), BPF rebuild + binary build.

## Generated files and tools

Source-of-truth inputs:

- `internal/abi/abi.yaml` for schema, tracepoint metadata, and kernel-event layout
- `internal/ebpf/collector.bpf.c` for BPF programs and maps
- `internal/abi/gen/` for the ABI generator
- `internal/ebpf/gen.go` for the `bpf2go` invocation

Generated outputs are checked in but never hand-edited:

- `internal/abi/kernel_event.gen.h`
- `internal/abi/schema_gen.go`
- `internal/abi/tracepoints_gen.go`
- `internal/storage/schema_gen.go`
- `internal/ebpf/collector_bpfel.go`
- `internal/ebpf/collector_bpfel.o`

Commands:

- `make generate` runs `go generate ./internal/abi/` and updates ABI/schema outputs.
- `make ebpf` runs `make generate`, then `go generate ./internal/ebpf/` through `bpf2go`; it needs Linux/CI clang with a BPF target.
- `make verify-generated` checks embedded hashes on ABI-generated files and catches hand edits without rebuilding BPF.
- `go test ./...` also runs generated-file drift tests.

Transport wording: the collector uses `BPF_MAP_TYPE_RINGBUF` plus
`ringbuf.NewReader`. Kernel reservation failures are counted in the
`ringbuf_drops` map and attributed to `Event.DroppedBefore` with bounded delay.

## Hard rules

**CLI.** Global flags before the subcommand. `start` re-execs
`daemon --foreground` with those flags forwarded — keep that ordering.
Defaults: DB `~/.local/share/ltm/ltm.db`, PID `~/.local/run/ltm.pid`.

**Events.** Everything is a `storage.Event`. Keep category/action strings
stable (`process`/`file`/`network`/`memory`/`block`; `exec`/`exit`/`open`/
`write`/`read`/`rename`/`unlink`/`bind`/`connect`/`listen`/…). Bump
`abi.SchemaVersion` only for incompatible replay/query changes. Update
`internal/abi/abi.yaml` when the `events` schema changes; `SchemaDoc` and the
storage DDL are generated from it (shown by `ltm query sql` and embedded in
agent prompts).

**Storage.** `Open` = sole writer (WAL, `MaxOpenConns(1)`). `OpenReadOnly` for
every read path (`query_only=ON`); error if the DB file is missing — never
create on a read path. Extend `Filter` instead of new one-off `EventsByX`
helpers. `DroppedBefore` is additive (`SUM`); do not overwrite. `watch` uses
`EventsAfterID` / `LatestEventID` — keep them id-ordered and cheap.

**eBPF.** Handwritten inputs: `collector.bpf.c`, `internal/abi/abi.yaml`,
loader `collector_linux.go`/`attach_linux.go`/`decode_linux.go`/`proc_linux.go`,
stub `collector_stub.go`. x86_64 only (`__TARGET_ARCH_x86`). Headers under
`headers/` are minimal stubs. Run `make generate` after editing
ABI/schema/tracepoint metadata; run `make ebpf` on Linux after BPF or ABI layout
edits. The kernel-to-userspace event transport is a BPF ring buffer; preserve
drop accounting when changing it. CI rebuilds too. No simulated collector —
use `ltm benchmark`.

**Agent.** `LTM_AGENT` / `--agent`: `claude|codex|cursor|gemini|auto|<custom>`.
SQL runs only via `OpenReadOnly` + `RawSQL`; `RawSQL` itself rejects any
multi-statement input (a lone `PRAGMA query_only=OFF` chained before a write
would otherwise defeat the read-only guard mid-script), and `ExtractSQL`
additionally rejects non-SELECT agent output. Agent failure → stderr warning +
`internal/query` templates.
Unit tests use fake shell scripts — never call a real agent CLI.

**Style.** Small focused diffs; extend existing engines; metadata only (no file
contents); respect ignore paths. No `Co-Authored-By` trailers. No
`replace` directives for `github.com/cilium/ebpf`. No secrets or one-off host
paths. Do not change default ignore paths without updating `docs/security.md`.

## Manual check

Linux + root:

```bash
sudo ./bin/ltm start
./bin/ltm status
echo test >> /tmp/probe.txt
./bin/ltm timeline --since 10m
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /tmp/probe.txt?"
./bin/ltm query sql "SELECT category, count(*) FROM events GROUP BY category"
sudo ./bin/ltm stop
```

Any OS: `./bin/ltm benchmark --count 500`, then timeline/diff/query against that DB.
