# Architecture

One contract: `storage.Event`. Collectors emit events; the store persists them;
diff/query/agent read them back. Nothing downstream of the store knows how an
event was collected.

Related: [CLI](cli.md) · [querying](querying.md) · [recording](recording.md) ·
[security](security.md)

## Pipeline

Recording has two planes that meet at the BPF ring buffer, then a purely
userspace path into SQLite:

```text
                         KERNEL SPACE
 +------------------------------------------------------------------+
 |  Linux tracepoints (sched / syscalls / block / ...)              |
 |       |                                                          |
 |       v                                                          |
 |  DATA PLANE  (collector.bpf.c → BPF bytecode)                    |
 |    should_skip / ignore /proc/sys/dev                            |
 |    reserve_event → fill ltm_kernel_event → submit                |
 |       |                                                          |
 |       v                                                          |
 |  events  BPF_MAP_TYPE_RINGBUF   (kernel-managed RAM)             |
 |  (+ scratch, fd_path, pending_open, ringbuf_drops, self_pid)     |
 +-------------------------------^----------------------------------+
                                 |
              setup: bpf() load / map create / attach
              steady: ringbuf.Reader.Read (map FD)
                                 |
                         USER SPACE
 +-------------------------------v----------------------------------+
 |  CONTROL PLANE  (internal/ebpf via cilium/ebpf)                  |
 |    embed collector_bpfel.o (ELF)                                 |
 |    NewCollection → attachTracepoints → ringbuf.NewReader         |
 |    decode RawSample → storage.Event                              |
 |       |                                                          |
 |       v  EventSource out channel                                 |
 |  FILTER   internal/collector                                     |
 |    ignore-path rules, bounded buffer, drop counter               |
 |       |                                                          |
 |       v                                                          |
 |  QUEUE    ingest chan (daemon entrance queue)                    |
 |       |                                                          |
 |       v                                                          |
 |  BATCH    daemon eventBatcher (size- or time-chunk)              |
 |    join producer → close(ingest) → final flush on shutdown       |
 |       |                                                          |
 |       v                                                          |
 |  STORE    internal/storage SQLite (single WAL writer)            |
 +------------------------------------------------------------------+

 READ PATH (no eBPF): timeline / watch / query / agent
   → OpenReadOnly → Filter / EventsAfterID / RawSQL
```

At start the control plane loads the embedded ELF and attaches programs. After
that, the data plane alone runs on each hook; the control plane only drains the
ring buffer and hands `storage.Event`s to the rest of the pipeline.

| Stage | Package | Role |
|---|---|---|
| ABI | `internal/abi` | Handwritten `abi.yaml` plus generated schema/version constants, tracepoint table, and kernel-event header used by storage, CLI help, agent prompts, and BPF compilation. |
| Capture | `internal/ebpf` | Userspace **control plane** for kernel↔userspace: `bpf()` load/attach of the embedded ELF, then drain the kernel `events` ring buffer into `storage.Event`. In-kernel **data plane** (`collector.bpf.c`) writes that buffer on tracepoints. Linux only; non-Linux stub errors. Rebuild with `make ebpf`. |
| Filter | `internal/collector` | Drop ignored path prefixes (userspace list; BPF only filters `/proc`/`/sys`/`/dev`). Bounded channel; overflow increments a dropped counter. |
| Queue + Batch | `internal/daemon` | Buffered `ingest` entrance queue decouples capture from SQLite. A confined `eventBatcher` (for-select) chunks by size or flush period into one transaction. Every `InsertEvents` uses a fresh Background timeout (not the run ctx) so cancel cannot abort a mid-flush write. On shutdown: cancel, **join the collector (producer)**, `close(ingest)`, then let flushLoop read to close and persist. |
| Store | `internal/storage` | SQLite (`modernc.org/sqlite`, no CGo). Daemon holds the only writer (`Open`, WAL, `MaxOpenConns(1)`). Every read path uses `OpenReadOnly` + `PRAGMA query_only=ON`. |

`Event.DroppedBefore` attributes kernel ring-buffer loss and collector overflow
to the next persisted row (additive; totals are `SUM(dropped_before)`).

## Reading

| Package | Role |
|---|---|
| `internal/diff` | Machine-state delta between two timestamps (processes, file mods, hot writers, sockets). |
| `internal/query` | Deterministic templates for common plain-English questions. |
| `internal/agent` | Optional external CLI (`LTM_AGENT` / `--agent`) that emits SQL; `ExtractSQL` keeps a single `SELECT`; always executed via read-only `RawSQL`. Failure → warn and fall back to templates. |
| CLI | `timeline` / `watch` / `query sql` go straight through `storage.Filter`, `EventsAfterID`, or `RawSQL`. |

`ltm benchmark` writes synthetic events through the store only — there is no
simulated collector.

## Package map

```text
cmd/ltm          thin main → cli.Execute
internal/cli     flags, subcommands, daemon spawn (Setsid)
internal/abi     abi.yaml + generated schema/tracepoint/kernel ABI
internal/daemon  service lifecycle + ingest queue + confined flushLoop
internal/collector  ignore rules + fan-in buffer
internal/ebpf    control plane (load/attach/read) + BPF C data plane + stub
internal/storage Event, Filter, SQLite
internal/diff    time-window summary
internal/query   NL templates
internal/agent   NL → SQL bridge
```
