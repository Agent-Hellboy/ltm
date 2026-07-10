# CLI reference

Global flags must appear **before** the subcommand:

```bash
ltm --db /tmp/ltm.db --json timeline --since 10m --category file
```

## Global flags

| Flag | Default | Meaning |
|---|---|---|
| `--db` | `~/.local/share/ltm/ltm.db` | SQLite database path |
| `--pidfile` | `~/.local/run/ltm.pid` | Recorder PID file |
| `--json` | off | Machine-readable output on read commands (overridable per command) |
| `--ignore-path` | (see [recording](recording.md)) | Extra path prefix to skip while recording (repeatable) |
| `-v` / `--version` | | Same as `ltm version` (accepted before subcommand parse) |

## Time values

Accepted by `--since`, `--until`, `--from`, `--to`, `--older-than`:

- `now`
- Go duration relative to now, subtracted: `5m`, `1h`, `720h`
- Absolute local time: `2006-01-02 15:04`, `2006-01-02 15:04:05`, RFC3339

## Commands

### `start` / `stop` / `status`

```bash
sudo ltm start
ltm status          # alive, event counts, dropped, last event time
sudo ltm stop
```

`start` re-execs as `daemon --foreground`, writes the pidfile, and `Setsid`s so
the process survives the launching shell. Needs root / BPF caps (see
[security](security.md)).

### `timeline`

Newest-first event list with AND filters. Repeatable flags OR within that field.

| Flag | Default | Notes |
|---|---|---|
| `--since` | `1h` | |
| `--until` | `now` | |
| `--limit` | `200` | |
| `--pid` / `--uid` / `--comm` / `--category` / `--action` | | repeatable |
| `--path` / `--exe` | | SQL `LIKE` (`%` / `_` wildcards) |
| `--json` | | |

```bash
ltm timeline --since 30m --category file --action write --path '/etc/%'
ltm timeline --comm nginx --comm php-fpm --limit 50
```

### `watch`

Poll for new rows by increasing `id` (high-water mark).

| Flag | Default | Notes |
|---|---|---|
| `--interval` | `1s` | Poll period |
| `--since` | (none) | Backfill from this time, then tail; default is only new events |
| `--limit` | `500` | Max events per poll |
| `--comm` / `--pid` / `--category` | | Client-side filter on each batch |

```bash
ltm watch --interval 500ms --category network
ltm watch --since 2m --comm curl
```

### `diff`

Summarize machine-state change in `[from, to]`:

- new / exited processes (creation deduped by pid)
- modified files (write, rename, chmod, chown, mkdir, truncate — not reads)
- deleted files (unlink, rmdir)
- new listeners, outbound connects
- hot writers (write actions only)
- restarts

```bash
ltm diff --from 1h --to now
ltm --json diff --from 2026-07-10T10:00:00Z --to now
```

### `query` / `query sql` / `sql`

See [querying.md](querying.md).

```bash
ltm query "who modified /tmp/probe.txt?"
ltm query --agent auto "top writers in the last hour"
ltm query sql "SELECT comm, count(*) n FROM events GROUP BY comm ORDER BY n DESC LIMIT 10"
ltm sql                                   # print schema
```

### `prune`

```bash
ltm prune --older-than 720h    # default 30 days; then VACUUM
```

### `benchmark`

Writes synthetic events through the store (no eBPF). Useful on any OS / CI.

```bash
ltm --db /tmp/demo.db benchmark --count 1000
```

### `version`

Prints version, commit, build date, Go version, OS/arch. `--json` supported.
