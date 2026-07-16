# Security

Related: [recording](recording.md) · [querying](querying.md) ·
[architecture](architecture.md)

## Privileges

| Action | Needs |
|---|---|
| `ltm start` (record) | root, or `CAP_BPF` + `CAP_PERFMON` |
| timeline / watch / diff / query / sql / status | none — opens the DB read-only |

`start` re-execs as `daemon --foreground` and detaches (`Setsid`) so the
recorder survives the launching shell.

## What is stored

Metadata only: pid/uid/comm/exe, paths, addresses/ports, category/action,
timestamps, small JSON `metadata`/`raw`. **No file contents, no payloads.**

Ignore prefixes (recorder only):

- **BPF:** `/proc`, `/sys`, `/dev` (plus the daemon PID, and any process whose
  program name is `ltm` — see below)
- **Userspace:** the above, plus `/var/cache/apt`, `/var/cache/dnf`,
  `/var/cache/pacman`, `$HOME/.cache`, `$HOME/Library/Caches`, and the
  recorder's own runtime files (see below)

Add more with `--ignore-path` (repeatable). Changing defaults → update this file.

## Self-capture guard (feedback loop)

`ltm status`/`query`/`timeline` are separate short-lived processes that read the
SQLite store. Without a guard, those reads are captured, stored, and re-read —
a self-amplifying loop that floods the ring buffer and inflates the drop count
(a single query has produced ~9k events and ~15k drops in one second). Two
layers prevent it, and neither needs any `--ignore-path`:

- **Kernel (`should_skip` in `collector.bpf.c`):** skips any task whose comm is
  `ltm`. This shuts the loop at the source — the daemon's own PID plus every
  sibling `ltm` invocation — before an event is reserved, so no ring-buffer
  pressure and no drops. **Blind spot:** a process literally named `ltm`
  (comm is truncated to 15 bytes) is invisible to the recorder. This is a
  deliberate, spoofable trade-off; renaming the binary defeats it.
- **Userspace (`daemon.withSelfIgnores`):** auto-adds the DB path, its
  `-wal`/`-shm`/`-journal` sidecars, and the pid file to the ignore rules, as
  defense in depth for any non-`ltm` reader of the store.

`ltm status` flags a suspected loop: when events are being dropped it prints the
busiest recent producers and, if `ltm` itself is among them (i.e. a recorder
predating this guard), says so.

## Write isolation

- Single WAL writer in the daemon (`Open`).
- Every read path, including `ltm query sql` / `ltm sql`, uses `OpenReadOnly`
  with `PRAGMA query_only=ON` — `INSERT`/`UPDATE`/`DELETE`/DDL fail at SQLite.
- Agent-generated SQL is additionally rejected unless it is one `SELECT`
  (`internal/agent.ExtractSQL`). Keep both layers.

## Kernel vs userspace

The BPF program is intentionally small: emit records, skip noisy prefixes, the
daemon PID, and the recorder's own comm (`ltm`). Filtering, enrichment,
batching, drop accounting, and storage live in userspace.
