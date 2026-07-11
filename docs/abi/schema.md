# Event Schema ABI

This document defines the persisted ABI for `ltm` query consumers.

The durable contract is the SQLite `events` table plus the meaning of its
columns. Collectors may vary, but anything written to the store must preserve
these semantics.

Current schema version: `1`

## Stability rules

- `category` and `action` strings are part of the public contract. Keep them
  stable.
- Adding a new column is a schema change and must update this document.
- Reinterpreting an existing column incompatibly requires a `SchemaVersion`
  bump.
- `metadata` may gain new keys over time. Existing keys should keep their
  meaning.
- `raw` is best-effort source detail, not the preferred stable query surface.

## Table: `events`

One row per collected event, persisted in insertion order.

| Column | Type | Meaning | Notes |
|---|---|---|---|
| `id` | `INTEGER` | Row id and insertion order | Primary key; `watch` uses it as a cursor |
| `ts` | `INTEGER` | Event timestamp in Unix nanoseconds | Format with `datetime(ts/1e9,'unixepoch')` |
| `category` | `TEXT` | High-level event family | `process`, `file`, `network`, `memory`, `block` |
| `action` | `TEXT` | Event verb inside a category | Examples: `exec`, `exit`, `open`, `write`, `connect`, `listen` |
| `pid` | `INTEGER` | Process id associated with the event | Usually the actor pid |
| `ppid` | `INTEGER` | Parent process id | Best effort for non-fork events |
| `uid` | `INTEGER` | Effective user id | `0` when unknown |
| `comm` | `TEXT` | Kernel process name | Short command name, not full argv |
| `exe` | `TEXT` | Resolved executable path | Commonly set for `process/exec` |
| `container_id` | `TEXT` | Container identifier | Empty when unavailable |
| `cgroup_path` | `TEXT` | Cgroup path | Empty when unavailable |
| `path` | `TEXT` | Primary file path or overloaded field for some sources | Empty when not applicable |
| `old_path` | `TEXT` | Source path for rename-like events | Empty otherwise |
| `local_addr` | `TEXT` | Local IP address | Usually IPv4 today |
| `local_port` | `INTEGER` | Local TCP/UDP port | `0` when not applicable |
| `remote_addr` | `TEXT` | Remote IP address | Empty when unknown |
| `remote_port` | `INTEGER` | Remote TCP/UDP port | `0` when not applicable |
| `remote_host` | `TEXT` | Resolved hostname | Empty unless enriched upstream |
| `target_pid` | `INTEGER` | Target pid for process-directed actions | Commonly used for `process/kill` |
| `exit_code` | `INTEGER` | Process exit status | `0` when not populated |
| `dropped_before` | `INTEGER` | Number of events lost immediately before this row | Additive; sum it for totals |
| `metadata` | `TEXT` | JSON object for structured extras | Query with `json_extract(metadata, '$.key')` |
| `raw` | `TEXT` | JSON object containing raw source detail | Best-effort debug surface |

## Indexes

The current schema creates these indexes:

- `ts`
- `(pid, ts)`
- `path`
- `(category, action, ts)`

Consumers should not assume additional indexes exist.

## Category and action conventions

`category` is intentionally small and coarse-grained:

- `process`
- `file`
- `network`
- `memory`
- `block`

`action` is a stable string within that category. Common values include:

- `process`: `exec`, `exit`, `fork`, `clone`, `kill`
- `file`: `open`, `close`, `read`, `write`, `rename`, `unlink`, `mkdir`, `chmod`
- `network`: `socket`, `connect`, `bind`, `listen`, `accept`, `send`, `recv`, `shutdown`
- `memory`: `mmap`, `munmap`, `mprotect`
- `block`: `io`

New values may be added, but existing values should not change meaning.

## Metadata conventions

`metadata` carries structured data that does not justify a top-level column.

Common keys today:

| Key | Meaning | Common sources |
|---|---|---|
| `syscall_nr` | Linux syscall number | most syscall-derived events |
| `fd` | file descriptor | fd-oriented file and network events |
| `aux` | small action-specific integer | mode bits, signal number, backlog, domain |
| `dev` | block device id | `block/io` |
| `nr_sector` | sector count | `block/io` |
| `rwbs` | block read/write flag string | `block/io` |
| `listen_fd` | listening socket fd | `network/listen` when address is unavailable |

Rules:

- prefer a top-level column if a field becomes broadly useful and stable
- do not overload an existing metadata key with a new meaning
- action-specific metadata is acceptable if documented here

## Known capture semantics

The storage ABI is stable, but values are still shaped by current capture
behavior:

- many syscall events are emitted on syscall entry, so byte counts are often
  requested sizes rather than completed sizes
- some fd-oriented events may lack `path`
- IPv6 addresses may be absent even for valid network activity
- block events reuse generic transport fields before userspace normalization

Those are recording semantics, not permission to change the column meanings.

## Example queries

```sql
SELECT datetime(ts/1e9,'unixepoch') AS t, comm, path
FROM events
WHERE category = 'file' AND action = 'write'
ORDER BY ts DESC
LIMIT 20;
```

```sql
SELECT comm, remote_addr, remote_port
FROM events
WHERE category = 'network' AND action = 'connect'
ORDER BY ts DESC
LIMIT 50;
```
