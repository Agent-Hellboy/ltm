# ltm

`ltm` is a machine-history debugger for Linux. It records process, file, and network metadata through a real eBPF collector (syscall tracepoints).

## What works now

- `ltm start` — begin recording via eBPF syscall tracepoints (needs root; Linux only)
- `ltm stop`
- `ltm status`
- `ltm timeline --since 1h [--pid] [--uid] [--comm] [--category] [--action] [--path] [--exe] [--until] [--limit]`
- `ltm watch [--interval 1s] [--since 10m] [--category] [--comm] [--pid]` — live tail of new events
- `ltm diff --from "2026-07-08 14:00" --to now`
- `ltm query "who modified /etc/nginx/nginx.conf?"` — plain English; uses a configured agent CLI when available, deterministic templates otherwise
- `ltm query sql "SELECT comm, count(*) FROM events GROUP BY comm ORDER BY 2 DESC"` — arbitrary read-only SQL; run with no query to print the schema (`ltm sql` is a shorthand)
- `ltm prune --older-than 720h`
- `ltm benchmark --count 1000`
- `ltm version` — build version, commit, and platform

## Build

```bash
go test ./...
go build -o bin/ltm ./cmd/ltm
```

On Linux, rebuild the embedded BPF object after changing `internal/ebpf/collector.bpf.c`:

```bash
make ebpf
```

## Usage (Linux, root)

```bash
sudo ./bin/ltm start                 # begin recording (eBPF)
./bin/ltm status
echo test >> /etc/some.conf          # do things on the machine
./bin/ltm timeline --since 5m
./bin/ltm watch --interval 1s        # live tail; Ctrl-C to stop
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /etc/some.conf?"
sudo ./bin/ltm stop
```

Recording requires root (or `CAP_BPF` + `CAP_PERFMON`); the read/query commands do not. Use `ltm benchmark` to populate a database with synthetic events without recording.

## eBPF coverage

The Linux collector attaches ~60 tracepoints across:

- **Process:** exec, exit, fork, clone, kill
- **Files:** open/close, read/write, truncate, unlink, rename, link, symlink, mkdir, chmod, chown, stat, access, pipe, dup, sendfile, splice, copy_file_range
- **Memory:** mmap, munmap, mprotect
- **Network:** socket, connect, bind, listen, accept, sendto/recvfrom, sendmsg/recvmsg, shutdown
- **Block:** `block_rq_issue` (real disk I/O requests)
- **Sched:** process fork and exit

BPF-side filters skip `/proc`, `/sys`, `/dev` and the daemon's own PID. FD-to-path tracking links read/write syscalls back to opened files.

See `internal/ebpf/tracepoints_linux.go` for the full hook list.

## Storage

Events live in a single SQLite database (`~/.local/share/ltm/ltm.db` by default), written in WAL mode by the daemon's writer connection. Every other command (`status`, `timeline`, `diff`, `query`, `sql`) opens the database read-only with `PRAGMA query_only=ON`, so reads never contend with the writer and can never accidentally mutate the log.

Query power is exposed two ways:

- Structured filters on `ltm timeline`: `--pid`, `--uid`, `--comm`, `--category`, `--action` are repeatable; `--path` and `--exe` take SQL `LIKE` patterns (`%` wildcard); `--since`/`--until` accept a duration (`10m`) or absolute time.
- `ltm query sql "<SELECT ...>"` for anything the filters don't cover — arbitrary read-only SQL against the `events` table. Run it with no query to print the table schema and example queries. `ltm sql` is a shorthand.

`ltm prune --older-than <duration>` deletes events past a cutoff and reclaims disk space with `VACUUM`.

## Agent-assisted queries

`ltm query "<plain English question>"` can hand the question to a locally installed coding agent, which writes the SQL for you:

```bash
export LTM_AGENT=claude          # or: codex, cursor, gemini, auto, or a custom command
ltm query "which process wrote to files the most in the last day?"
```

```text
[claude] SELECT comm, exe, pid, COUNT(*) AS write_count FROM events WHERE category = 'file' ...
comm             pid   write_count
demo-worker-0    4200  1
...
```

- Configure with the `--agent` flag or the `LTM_AGENT` environment variable. Known names: `claude` (Claude Code), `codex` (OpenAI Codex), `cursor` (cursor-agent), `gemini` (Gemini CLI). `auto` picks the first one found on PATH. Anything else is treated as a custom command; the prompt is appended as the final argument.
- The generated SQL is printed (stderr in table mode, in the payload with `--json`) so you always see what ran.
- The agent's SQL executes on the read-only connection (`PRAGMA query_only=ON`) and is rejected unless it is a single `SELECT` — a misbehaving agent cannot modify the event log.
- With no agent configured, or if the agent fails, `ltm query` falls back to the built-in deterministic templates.

## Repository layout

```text
cmd/ltm/          CLI binary entrypoint
internal/
  cli/            commands and global flags
  daemon/         background service and batching
  collector/      ingestion, ignore rules, buffering
  ebpf/           BPF program, embedded object, Linux loader
  storage/        SQLite-backed event store, filters, prune
  diff/           machine-state diff engine
  query/          deterministic query templates
tests/            integration script and fixtures
docs/             architecture and security notes
```

## Security

- Recording requires root or `CAP_BPF`, `CAP_PERFMON`, and often `CAP_SYS_ADMIN` depending on kernel and distro policy.
- The recorder stores metadata only.
- No file contents are captured.
- Ignore rules include `/proc`, `/sys`, `/dev`, browser cache directories, and package cache directories.

## Integration

`tests/integration.sh` records real activity with the eBPF collector and exercises the query surface end to end; it needs a Linux host with root. CI runs unit tests on Ubuntu and macOS, plus the integration script on Ubuntu.
