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
- **Drops** — under overload, kernel perf-buffer loss and collector channel
  overflow increment `dropped_before` on the next persisted row
  (`SUM(dropped_before)` for totals).
- **Metadata only** — no file contents or packet payloads (see
  [security.md](security.md)).

## Lifecycle

```bash
sudo ltm start
ltm status                 # alive, counts, dropped, last event
# … workload …
sudo ltm stop              # drains buffer, final flush, then exits
```

On shutdown the flush loop must finish before the store is closed; otherwise the
last batch can be lost. That path is covered by daemon tests.
