# ltm

`ltm` is a machine-history debugger for Linux. It records process, file, and network metadata through a real eBPF collector (syscall tracepoints) or a demo mode for local development without kernel privileges.

## What works now

- `ltm start --mode demo` — synthetic events for development and CI
- `ltm start --mode ebpf` — real syscall tracepoint collection on Linux
- `ltm stop`
- `ltm status`
- `ltm timeline --since 1h`
- `ltm diff --from "2026-07-08 14:00" --to now`
- `ltm query "who modified /etc/nginx/nginx.conf?"`
- `ltm benchmark --count 1000`

## Build

```bash
go test ./...
go build -o bin/ltm ./cmd/ltm
```

On Linux, rebuild the embedded BPF object after changing `internal/ebpf/collector.bpf.c`:

```bash
make ebpf
```

## Demo flow

```bash
./bin/ltm start --mode demo
./bin/ltm status
./bin/ltm timeline --since 1h
./bin/ltm diff --from "2026-07-08 14:00" --to now
./bin/ltm query "who modified /tmp/ltm-demo.txt?"
./bin/ltm stop
```

## eBPF flow (Linux, root)

```bash
sudo ./bin/ltm start --mode ebpf
./bin/ltm status
echo test >> /tmp/ltm-demo.txt
./bin/ltm query "who modified /tmp/ltm-demo.txt?"
sudo ./bin/ltm stop
```

## eBPF coverage (`--mode ebpf`)

The Linux collector attaches ~60 tracepoints across:

- **Process:** exec, exit, fork, clone, kill
- **Files:** open/close, read/write, truncate, unlink, rename, link, symlink, mkdir, chmod, chown, stat, access, pipe, dup, sendfile, splice, copy_file_range
- **Memory:** mmap, munmap, mprotect
- **Network:** socket, connect, bind, listen, accept, sendto/recvfrom, sendmsg/recvmsg, shutdown
- **Block:** `block_rq_issue` (real disk I/O requests)
- **Sched:** process fork and exit

BPF-side filters skip `/proc`, `/sys`, `/dev` and the daemon's own PID. FD-to-path tracking links read/write syscalls back to opened files.

See `internal/ebpf/tracepoints_linux.go` for the full hook list.

## Repository layout

```text
cmd/ltm/          CLI binary entrypoint
internal/
  cli/            commands and global flags
  daemon/         background service and batching
  collector/      ingestion, ignore rules, buffering
  ebpf/           BPF program, embedded object, Linux loader
  storage/        append-only event store
  diff/           machine-state diff engine
  query/          deterministic query templates
tests/            integration script and fixtures
docs/             architecture and security notes
```

## Security

- Demo mode does not require kernel privileges.
- eBPF mode requires root or `CAP_BPF`, `CAP_PERFMON`, and often `CAP_SYS_ADMIN` depending on kernel and distro policy.
- The recorder stores metadata only.
- No file contents are captured.
- Ignore rules include `/proc`, `/sys`, `/dev`, browser cache directories, and package cache directories.

## Integration

`tests/integration.sh` exercises the CLI and demo daemon flow. CI runs unit tests on Ubuntu and macOS, plus the integration script on Ubuntu.
