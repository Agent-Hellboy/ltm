# ltm

Machine-history debugger for Linux. Records process, file, network, memory, and
block I/O via eBPF, stores metadata in SQLite, and answers timeline / diff /
plain-English / SQL questions about what happened on the box.

## Quick start

```bash
go build -o bin/ltm ./cmd/ltm

sudo ./bin/ltm start                       # record (eBPF; root, Linux/x86_64)
./bin/ltm timeline --since 5m
./bin/ltm watch                            # live tail; Ctrl-C to stop
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /etc/some.conf?"
sudo ./bin/ltm stop
```

Recording needs root (or `CAP_BPF` + `CAP_PERFMON`). Querying does not.
Without a live Linux recorder: `ltm benchmark --count 1000` seeds synthetic
events into a DB that you can inspect with `timeline`, `diff`, and `query`.

Global flags go **before** the subcommand (`ltm --db /tmp/ltm.db status`).
Defaults: DB `~/.local/share/ltm/ltm.db`, PID `~/.local/run/ltm.pid`.
Add `--json` to any read command for machine-readable output.

## Commands

| Command | What it does |
|---|---|
| `start` / `stop` / `status` | control the recorder |
| `timeline` | filter by `--pid --uid --comm --category --action --path --exe --since --until --limit` (repeatable; `--path`/`--exe` are SQL `LIKE`) |
| `watch` | live tail (`--interval --since --category --comm --pid`) |
| `diff --from --to` | machine-state changes between two times |
| `query "<question>"` | plain English (templates, or an agent → SQL) |
| `query sql ["<SELECT>"]` | read-only SQL; no arg prints the schema (`ltm sql` works too) |
| `prune --older-than 720h [--vacuum]` | drop old rows, optionally reclaiming disk space |
| `benchmark --count N` | write N synthetic events (no eBPF) |
| `version` | build version, commit, platform |

## Storage and querying

One SQLite database, WAL writer held by the daemon. Every read path opens
read-only (`PRAGMA query_only=ON`) — queries never contend with the writer or
mutate the log. Metadata only; no file contents.

```bash
export LTM_AGENT=claude   # or codex, cursor, gemini, auto, or a custom command
ltm query "which process wrote to files the most today?"
```

Agent SQL is printed, then run on the read-only connection, and rejected unless
it is a single `SELECT`. No agent (or agent failure) → built-in templates.

## What gets recorded

~60 tracepoints: **process** (exec, exit, fork, clone, kill), **file**
(open/close, read/write, rename, unlink, link, symlink, mkdir, rmdir, chmod,
chown, stat, access, truncate, dup, pipe, …), **memory** (mmap, munmap,
mprotect), **network** (socket, connect, bind, listen, accept, send/recv,
shutdown), **block** (`block_rq_issue`).

BPF skips `/proc`, `/sys`, `/dev` and the daemon's own PID. The handwritten
tracepoint manifest lives in `internal/abi/abi.yaml`; the checked-in runtime
table is generated into `internal/abi/tracepoints_gen.go`. After editing
`collector.bpf.c`, rebuild with `make ebpf`. After editing ABI metadata in
`internal/abi/abi.yaml`, run `make generate` and then `make ebpf` if the
kernel-event layout or tracepoint table changed.

### Limitations

- **x86_64 recording only** — BPF is built `-D__TARGET_ARCH_x86`
  ([#2](https://github.com/Agent-Hellboy/ltm/issues/2)). Querying and
  benchmark-generated demo data do not require Linux recording support.
- **IPv4 addresses only** — IPv6 connects/binds are stored without a decoded address.
- **Byte counts** are the syscall's *requested* size (enter probe); short/failed
  I/O over-counts; `readv`/`writev`/`sendmsg`/`recvmsg` report `0`.
- **fd→path** covers fds ≤ 1024 and can misattribute after heavy PID reuse;
  higher fds are recorded without a path.

## Development

```bash
go test ./...                # local/unit tests
make generate                # regenerate ABI/schema outputs from abi.yaml
make ebpf                    # regenerate checked-in BPF object/bindings (Linux)
make integration             # real eBPF recording; Linux + root
```

Layout: `cmd/ltm` entrypoint; everything else under `internal/`
(`abi`, `cli`, `daemon`, `collector`, `ebpf`, `storage`, `agent`, `diff`, `query`).

The ABI/schema generator keeps event definitions, storage DDL, tracepoint
metadata, and kernel-facing structs tied to one source of truth:
`internal/abi/abi.yaml`. It is intentionally similar in spirit to CPython's
generated-code workflow: when adding new captured event/module surface, update
the manifest and regenerate checked-in outputs instead of hand-editing derived
Go or C files.

Docs: [`docs/`](docs/) (ABI, CLI, generated files, querying, recording,
architecture, security).
Contributor rules: [`AGENTS.md`](AGENTS.md).
