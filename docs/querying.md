# Querying

All read paths open the DB with `PRAGMA query_only=ON`. Nothing here can mutate
the log — including agent-generated SQL.

## Timeline and watch

Structured filters: see [cli.md](cli.md). Prefer `timeline` for ad-hoc slices;
`watch` for a live tail by event id.

## Plain-English `ltm query`

Resolution order:

1. If `--agent` / `LTM_AGENT` resolves to a CLI, ask it for one `SELECT`, print
   the SQL, run it read-only. On failure → warn on stderr and continue to (2).
2. Deterministic templates in `internal/query`.
3. Otherwise free-text AND search across common columns (`QueryText`).

### Built-in templates

| Pattern (case-insensitive) | Behavior |
|---|---|
| `who modified <path>?` | file write/rename/unlink on that path |
| `… restarted …` / `before … restarted` | process exec/exit/fork/clone; optional process name token |
| `connected to …` / `connected …` | connects; filter by IPv4 or hostname token when present |
| `opened port …` | bind/listen matching the port |
| `show activity for pid N` | all events for that pid |
| `show activity for file <path>` | events touching path / old_path |

Examples:

```bash
ltm query "who modified /etc/nginx/nginx.conf?"
ltm query "what changed before nginx restarted?"
ltm query "what connected to 1.1.1.1?"
ltm query "show activity for pid 4321"
```

### Agent bridge

```bash
export LTM_AGENT=claude          # or: codex | cursor | gemini | auto
# or a custom argv: export LTM_AGENT="my-wrapper --flag"
ltm query --agent auto "which processes wrote under /var/log today?"
```

| Spec | Meaning |
|---|---|
| `claude` / `codex` / `cursor` / `gemini` | known CLI must be on `PATH` |
| `auto` | first of those found on `PATH` |
| other | `strings.Fields` as argv; prompt appended as final arg |
| empty | skip agent, use templates only |

Guards: `ExtractSQL` requires a single `SELECT` / `WITH … SELECT` (no
multi-statement; rejects `WITH … DELETE`). Execution always goes through
`OpenReadOnly` + `RawSQL`.

## SQL

```bash
ltm query sql                         # print schema (SchemaDoc)
ltm query sql "SELECT …"
ltm sql "SELECT …"                    # shorthand
ltm --json query sql "SELECT …"       # JSON rows
```

### Schema (`events`)

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER | insertion order; `watch` cursor |
| `ts` | INTEGER | unix **nanoseconds**; `datetime(ts/1e9,'unixepoch')` |
| `category` | TEXT | `process` \| `file` \| `network` \| `memory` \| `block` |
| `action` | TEXT | `exec`, `exit`, `open`, `write`, `connect`, … |
| `pid`, `ppid`, `uid` | INTEGER | |
| `comm` | TEXT | process name |
| `exe` | TEXT | resolved executable |
| `container_id`, `cgroup_path` | TEXT | when available |
| `path`, `old_path` | TEXT | file path / rename source |
| `local_addr`, `local_port`, `remote_addr`, `remote_port`, `remote_host` | | network |
| `target_pid`, `exit_code` | INTEGER | |
| `dropped_before` | INTEGER | events lost immediately before this row |
| `metadata`, `raw` | TEXT | JSON; use `json_extract(metadata, '$.key')` |

Indexes: `ts`, `(pid, ts)`, `path`, `(category, action, ts)`.

### Example queries

```sql
-- busiest writers in the last hour
SELECT comm, count(*) n
FROM events
WHERE category = 'file' AND action = 'write'
  AND ts > (unixepoch() - 3600) * 1000000000
GROUP BY comm
ORDER BY n DESC
LIMIT 20;

-- connects to a host
SELECT datetime(ts/1e9,'unixepoch') AS t, comm, remote_addr, remote_port, remote_host
FROM events
WHERE category = 'network' AND action = 'connect'
  AND (remote_host LIKE '%example.com%' OR remote_addr = '93.184.216.34')
ORDER BY ts DESC
LIMIT 50;

-- drops in the timeline
SELECT sum(dropped_before) AS dropped FROM events;
```
