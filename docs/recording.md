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

**BPF** (`collector.bpf.c`) drops only `/proc`, `/sys`, `/dev` (and the daemon's
own PID).

**Userspace** (`internal/collector`) drops those again, plus package-manager
caches and CLI defaults:

| Prefix | Where |
|---|---|
| `/proc`, `/sys`, `/dev` | BPF + userspace |
| `/var/cache/apt`, `/var/cache/dnf`, `/var/cache/pacman` | userspace |
| `$HOME/.cache`, `$HOME/Library/Caches` | userspace (CLI defaults) |

Add more at start time:

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

## systemd (optional)

`ltm start` is the default, portable way to run the recorder. Machines that
want it always on across reboots can instead run `daemon --foreground`
directly under systemd, using the unit in
[`contrib/systemd/ltm.service`](../contrib/systemd/ltm.service). This is
opt-in and Linux-only (recording already requires Linux); it doesn't replace
`ltm start`/`ltm stop`.

```bash
go build -o bin/ltm ./cmd/ltm
sudo install -m 0755 bin/ltm /usr/bin/ltm
sudo install -m 0644 contrib/systemd/ltm.service /etc/systemd/system/ltm.service
sudo systemctl daemon-reload
sudo systemctl enable --now ltm
```

```bash
systemctl status ltm                     # unit + process state, journal tail
sudo journalctl -u ltm -f                # follow recorder logs
ltm --db /var/lib/ltm/ltm.db status      # event counts, dropped, last event
ltm --db /var/lib/ltm/ltm.db timeline --since 10m

sudo systemctl stop ltm                  # graceful: same shutdown path as `ltm stop`
sudo systemctl disable ltm
```

Notes:

- The unit runs `daemon --foreground` as `Type=simple`, which is what it's
  designed for: no forking, no re-exec, systemd tracks the process directly
  and restarts it on failure.
- Needs root, or `CAP_BPF` + `CAP_PERFMON` **and** `CAP_DAC_READ_SEARCH` — the
  collector also reads tracepoint ids from
  `/sys/kernel/tracing/events/**/id`, which are mode `440 root:root`, so the
  two BPF caps alone aren't enough for a non-root user (every tracepoint open
  fails with "permission denied" and the daemon exits). The unit defaults to
  `User=root` (matching `sudo ltm start`) with a commented-out unprivileged
  variant carrying all three capabilities, verified on a 6.8 kernel. See
  [security.md](security.md).
- The `daemon` subcommand (unlike `start`) doesn't write a pidfile, so
  `ltm status`'s `alive` field won't reflect a systemd-managed process — use
  `systemctl status ltm` for liveness instead. Event counts/timeline/etc. via
  `ltm` still work against the same `--db` path either way.
- `StateDirectory=ltm` / `RuntimeDirectory=ltm` put the db under
  `/var/lib/ltm/` and the (unused) pidfile path under `/run/ltm/`; systemd
  creates/owns both.
- **Don't point `ltm start` (or a second instance) at the same `--db` as an
  active systemd-managed recorder.** Two writers on one SQLite db contend
  until a write blows past an internal deadline, both processes exit with
  `context deadline exceeded`, and `Restart=on-failure` crash-loops the
  service until only one recorder remains. No data corruption results
  (`PRAGMA integrity_check` stays `ok`), but it's a real outage of recording
  in the meantime — use a distinct `--db` for anything ad hoc, or stop the
  unit first.
