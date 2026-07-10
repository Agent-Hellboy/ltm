# ltm

`ltm` is a machine-history debugger for Linux. It records process, file, network, memory, and block-I/O activity via eBPF, then lets you query the timeline of what happened — for answering "what broke and why" and for post-incident forensics.

## Quick start

```bash
go build -o bin/ltm ./cmd/ltm

sudo ./bin/ltm start                       # record (eBPF; needs root, Linux only)
./bin/ltm timeline --since 5m
./bin/ltm watch                            # live tail; Ctrl-C to stop
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /etc/some.conf?"
sudo ./bin/ltm stop
```

Recording needs root (or `CAP_BPF` + `CAP_PERFMON`); querying doesn't. No live host? `ltm benchmark --count 1000` seeds a database with synthetic events.

## Commands

| Command | Description |
|---|---|
| `start` / `stop` / `status` | control and inspect the recorder |
| `timeline` | filter events by `--pid --uid --comm --category --action --path --exe --since --until --limit` (repeatable; `--path`/`--exe` take SQL `LIKE`) |
| `watch` | live tail of new events (`--interval --since --category --comm --pid`) |
| `diff --from --to` | machine-state changes between two times |
| `query "<question>"` | plain-English query (see below) |
| `query sql "<SELECT>"` | arbitrary read-only SQL; no arg prints the schema (`ltm sql` for short) |
| `prune --older-than 720h` | drop old events and `VACUUM` |
| `version` | build version, commit, platform |

Add `--json` to any read command for machine-readable output.

## Querying

Events live in one SQLite database (`~/.local/share/ltm/ltm.db`), written in WAL mode by the recorder. Every read command opens it read-only (`PRAGMA query_only=ON`), so queries never contend with the writer or mutate the log.

`ltm query "<plain English>"` can hand the question to a locally installed coding agent that writes the SQL for you:

```bash
export LTM_AGENT=claude   # or codex, cursor, gemini, auto, or a custom command
ltm query "which process wrote to files the most today?"
```

The generated SQL is printed before it runs, executes on the read-only connection, and is rejected unless it's a single `SELECT`. With no agent configured (or on failure), `query` falls back to built-in templates.

## eBPF coverage

~60 tracepoints across **process** (exec, exit, fork, clone, kill), **file** (open/close, read/write, rename, unlink, link, symlink, mkdir, rmdir, chmod, chown, stat, access, truncate, dup, pipe, …), **memory** (mmap, munmap, mprotect), **network** (socket, connect, bind, listen, accept, send/recv, shutdown), and **block** (`block_rq_issue`). BPF-side filters skip `/proc`, `/sys`, `/dev` and the daemon's own PID. See `internal/ebpf/tracepoints_linux.go` for the full list; rebuild the embedded object with `make ebpf` after editing `collector.bpf.c`.

The recorder stores metadata only — no file contents.

### Known limitations

- **x86_64 only** for recording — the BPF program is built `-D__TARGET_ARCH_x86` (see issue https://github.com/Agent-Hellboy/ltm/issues/2). Query features work anywhere.
- **IPv4 only** — AF_INET6 connects/binds are recorded as events but without the address decoded.
- **Read/write byte counts** are the syscall's *requested* size (read at `sys_enter`), so short/failed I/O over-counts; `readv`/`writev`/`sendmsg`/`recvmsg` report `0` bytes.
- **fd→path resolution** covers fds ≤ 1024 and can misattribute after heavy PID reuse; events for higher fds are recorded without a path.

## Development

```bash
go test ./...                # unit tests (any OS)
make integration             # records real activity via eBPF; needs Linux + root
```

Layout: `cmd/ltm` (entrypoint); `internal/` → `cli`, `daemon`, `collector`, `ebpf`, `storage`, `diff`, `query`. See `docs/` for architecture and security notes.
