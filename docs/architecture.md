# Architecture

One contract: `storage.Event`. Collectors emit events; the store persists them;
diff/query/agent read them back. Nothing downstream of the store knows how an
event was collected.

Related: [CLI](cli.md) · [querying](querying.md) · [recording](recording.md) ·
[security](security.md)

## Pipeline

```
eBPF tracepoints ──▶ ebpf.EventSource ──▶ collector ──▶ ingest queue ──▶ daemon.flushLoop ──▶ storage (SQLite)
                     kernel → Event       ignore +       entrance        size/time chunks      single WAL writer
                                          buffer + drop  (decouple)      (confined batcher)
```

| Stage | Package | Role |
|---|---|---|
| ABI | `internal/abi` | Handwritten `abi.yaml` plus generated schema/version constants, tracepoint table, and kernel-event header used by storage, CLI help, agent prompts, and BPF compilation. |
| Capture | `internal/ebpf` | Attach syscall/sched/block tracepoints; map each kernel record to `storage.Event`. Linux only; non-Linux stub errors. BPF object and Go bindings are generated/embedded; rebuild with `make ebpf`. |
| Filter | `internal/collector` | Drop ignored path prefixes (userspace list; BPF only filters `/proc`/`/sys`/`/dev`). Bounded channel; overflow increments a dropped counter. |
| Queue + Batch | `internal/daemon` | Buffered `ingest` entrance queue decouples capture from SQLite. A confined `eventBatcher` (for-select) chunks by size or flush period into one transaction. On shutdown: cancel, **join the collector (producer)**, `close(ingest)`, then let flushLoop read to close and persist with a **fresh** context so the cancelled run ctx cannot abort the final write. |
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
internal/ebpf    EventSource contract, BPF C, generated .o/.go bindings
internal/storage Event, Filter, SQLite
internal/diff    time-window summary
internal/query   NL templates
internal/agent   NL → SQL bridge
```
