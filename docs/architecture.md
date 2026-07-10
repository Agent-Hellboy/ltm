# Architecture

One contract: `storage.Event`. Collectors emit events; the store persists them;
diff/query/agent read them back. Nothing downstream of the store knows how an
event was collected.

## Pipeline

```
eBPF tracepoints ──▶ ebpf.RealCollector ──▶ collector ──▶ daemon.flushLoop ──▶ storage (SQLite)
                     kernel → Event         ignore +       batch TX              single WAL writer
                                            buffer + drop
```

| Stage | Package | Role |
|---|---|---|
| Capture | `internal/ebpf` | Attach syscall/sched/block tracepoints; map each kernel record to `storage.Event`. Linux only; non-Linux stub errors. BPF object is embedded (`collector_bpfel.o`); rebuild with `make ebpf`. |
| Filter | `internal/collector` | Drop ignored path prefixes (`/proc`, `/sys`, `/dev`, caches, extras via `--ignore-path`). Bounded channel; overflow increments a dropped counter. |
| Batch | `internal/daemon` | `flushLoop` writes batches in one transaction. On shutdown: stop sources, drain the buffer, flush with a **fresh** context (the cancelled run ctx must not abort the final write), then return so the caller can close the store. |
| Store | `internal/storage` | SQLite (`modernc.org/sqlite`, no CGo). Daemon holds the only writer (`Open`, WAL, `MaxOpenConns(1)`). Every read path uses `OpenReadOnly` + `PRAGMA query_only=ON`. |

`Event.DroppedBefore` attributes kernel perf-buffer loss and collector overflow
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
internal/daemon  service lifecycle + flushLoop
internal/collector  ignore rules + fan-in buffer
internal/ebpf    BPF C, embedded .o, RealCollector
internal/storage Event, Filter, SQLite
internal/diff    time-window summary
internal/query   NL templates
internal/agent   NL → SQL bridge
```
