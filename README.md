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

On Linux, rebuild the embedded BPF object after changing `ebpf/collector.bpf.c`:

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

## Architecture

- `ebpf/`: syscall tracepoint BPF program and Linux collector
- `collector/`: bounded ingestion and ignore-path filtering
- `daemon/`: background service, batching, graceful shutdown
- `storage/`: append-only local event store and query helpers
- `diff/`: machine-state diff engine
- `query/`: deterministic query templates
- `cli/`: command-line entry points
- `tests/`: unit and integration fixtures

## Security

- Demo mode does not require kernel privileges.
- eBPF mode requires root or `CAP_BPF`, `CAP_PERFMON`, and often `CAP_SYS_ADMIN` depending on kernel and distro policy.
- The recorder stores metadata only.
- No file contents are captured.
- Ignore rules include `/proc`, `/sys`, `/dev`, browser cache directories, and package cache directories.

## Integration

`tests/integration.sh` exercises the CLI and demo daemon flow. CI runs unit tests on Ubuntu and macOS, plus the integration script on Ubuntu.
