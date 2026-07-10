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

Default ignore prefixes (BPF + userspace): `/proc`, `/sys`, `/dev`,
`$HOME/.cache` (and `$HOME/Library/Caches` on macOS query hosts). Add more with
`--ignore-path` (repeatable). Changing defaults → update this file.

## Write isolation

- Single WAL writer in the daemon (`Open`).
- Every read path, including `ltm query sql` / `ltm sql`, uses `OpenReadOnly`
  with `PRAGMA query_only=ON` — `INSERT`/`UPDATE`/`DELETE`/DDL fail at SQLite.
- Agent-generated SQL is additionally rejected unless it is one `SELECT`
  (`internal/agent.ExtractSQL`). Keep both layers.

## Kernel vs userspace

The BPF program is intentionally small: emit records, skip noisy prefixes and
the daemon PID. Filtering, enrichment, batching, drop accounting, and storage
live in userspace.
