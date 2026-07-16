# Recording

`ltm start` loads an embedded BPF object, attaches tracepoints, and streams
`storage.Event` rows into SQLite. Query and benchmark features do not require a
Linux recorder; **recording itself is Linux/x86_64 only**.

## Requirements

- Linux, x86_64 (BPF built with `-D__TARGET_ARCH_x86`; see
  [#2](https://github.com/Agent-Hellboy/ltm/issues/2))
- root, or `CAP_BPF` + `CAP_PERFMON`
- Kernel with the relevant syscall/sched/block tracepoints (missing optional
  ones are skipped; if **none** attach, start fails)

Without a recorder host, seed data with `ltm benchmark` (see [cli.md](cli.md)).

## Coverage

Handwritten capture manifest: `internal/abi/abi.yaml` (generated runtime table:
`internal/abi/tracepoints_gen.go`). Categories and typical actions:

| Category | Hooks (summary) | Common `action` values |
|---|---|---|
| `process` | `sched_process_fork/exit`, `execve(at)`, `clone(3)`, `kill`/`tgkill` | `fork`, `clone`, `exec`, `exit`, `kill` |
| `file` | open/close, read/write (+`*v`, `pread`/`pwrite`), rename/unlink/link/symlink, mkdir/rmdir, chmod/chown, stat/access, truncate, dup/pipe, splice/sendfile, … | `open`, `close`, `read`, `write`, `rename`, `unlink`, `mkdir`, `rmdir`, … |
| `memory` | `mmap`, `munmap`, `mprotect` | `mmap`, `munmap`, `mprotect` |
| `network` | `socket`, `connect`, `bind`, `listen`, `accept(4)`, `send*`/`recv*`, `shutdown` | `socket`, `connect`, `bind`, `listen`, `accept`, `send`, `recv`, … |
| `block` | `block_rq_issue` (optional) | block request metadata in event fields |

Optional tracepoints (e.g. `clone3`, `mkdir`, `rmdir`, `block_rq_issue`,
`copy_file_range`) are skipped when absent instead of failing the whole session.

## Ignore paths

**BPF** (`collector.bpf.c`) drops only `/proc`, `/sys`, `/dev`, the daemon's
own PID, and any task whose comm is `ltm` (the self-capture guard — see
[security](security.md#self-capture-guard-feedback-loop)).

**Userspace** (`internal/collector`) drops those again, plus package-manager
caches, CLI defaults, and the recorder's own store/pid files:

| Prefix | Where |
|---|---|
| `/proc`, `/sys`, `/dev` | BPF + userspace |
| comm `ltm` | BPF |
| `/var/cache/apt`, `/var/cache/dnf`, `/var/cache/pacman` | userspace |
| `$HOME/.cache`, `$HOME/Library/Caches` | userspace (CLI defaults) |
| DB path + `-wal`/`-shm`/`-journal`, pid file | userspace (auto, per daemon) |

The DB and pid file are added automatically from the daemon's `--db`/`--pidfile`
values, so `ltm status`/`query` reading the store never feeds a capture loop —
no `--ignore-path` needed. Add more at start time:

```bash
sudo ltm --ignore-path /var/cache --ignore-path /tmp/scratch start
```

## Rebuild BPF

After editing `internal/ebpf/collector.bpf.c`:

```bash
make ebpf          # regenerate ABI header, then clang/bpf2go outputs
go build -o bin/ltm ./cmd/ltm
```

If you changed `internal/abi/abi.yaml` without touching BPF C, run
`make generate`; run `make ebpf` as well if the tracepoint set or kernel-event
layout changed. The generated `collector_bpfel.o` and `collector_bpfel.go` are
checked in, rebuilt in CI, and should not be hand-edited. Headers under
`internal/ebpf/headers/` are minimal stubs, not a full vmlinux/libbpf tree.

## Resource sampling (Phase 1)

Alongside the per-event activity log, the daemon runs a userspace sampler
(`internal/sample`) that reads `/proc` + PSI — no eBPF, Linux only. It writes
two tables, queryable with `ltm query sql` (see their columns in
`ltm query sql` with no argument):

- `system_samples` (~1s): CPU %, load, runnable/blocked, memory/swap,
  CPU/memory/I/O pressure (PSI avg10), aggregate disk and network throughput.
- `process_samples` (~5s): per-process CPU %, RSS, state, threads, cumulative
  I/O, cgroup — one row per process per tick.

`ltm status` prints the latest system sample as a one-liner. Rates (CPU %,
disk, network) are deltas over the interval since the previous sample, so the
first sample after start reports zero rates. Cadence is set on
`daemon.Config` (`SystemSampleEvery`/`ProcessSampleEvery`; negative disables a
sampler). `ltm prune` trims these tables with the same cutoff as `events` —
relevant because `process_samples` grows with process count.

## Limitations

- **Arch** — recording x86_64 only; arm64 binaries can query but not attach this
  object.
- **IPv4** — IPv6 connect/bind events are stored without a decoded address.
- **Byte counts** — enter-probe *requested* size; short/failed I/O over-counts;
  `readv`/`writev`/`sendmsg`/`recvmsg` report `0`.
- **fd→path** — map covers fds ≤ 1024; heavy PID reuse can misattribute; higher
  fds are recorded without a path.
- **Drops** — under overload, failed kernel ring-buffer reservations and
  collector channel overflow increment `dropped_before` on a later persisted row
  (`SUM(dropped_before)` for totals).
- **Metadata only** — no file contents or packet payloads (see
  [security.md](security.md)).

## Lifecycle

```bash
sudo ltm start
ltm status                 # alive, counts, dropped, last event
# … workload …
sudo ltm stop              # join producer, close ingest, final flush, then exits
```

On shutdown the service joins the collector before closing `ingest`, then waits for
the flush loop to finish before the store is closed; otherwise the last batch can be
lost. That path is covered by daemon tests.
