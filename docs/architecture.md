# Architecture

Everything is organized around one narrow contract: `storage.Event`. Collectors
produce events; the store persists them; the diff and query engines read them
back. Nothing downstream of the store knows how events were collected.

## Pipeline

```
eBPF tracepoints ──▶ ebpf.RealCollector ──▶ collector ──▶ daemon.flushLoop ──▶ storage (SQLite)
                     (kernel → Event)      (ignore +      (batches into        (WAL writer)
                                            buffering)     transactions)
```

1. `internal/ebpf` attaches syscall/sched/block tracepoints and converts each
   kernel record into a `storage.Event` (Linux only; a stub errors elsewhere).
2. `internal/collector` drops ignored paths (`/proc`, `/sys`, `/dev`, caches)
   and buffers, counting events shed under backpressure.
3. `internal/daemon` batches events and writes each batch in one transaction;
   on shutdown it drains the buffer and flushes with a fresh context.
4. `internal/storage` owns the SQLite database. The daemon holds the single
   WAL writer; every read path opens read-only with `PRAGMA query_only=ON`.

## Reading

- `internal/diff` summarizes machine-state change between two timestamps.
- `internal/query` answers structured filters, raw read-only SQL, and
  plain-English questions — the last optionally delegated to a coding-agent CLI
  (`internal/agent`) that emits SQL, always validated down to a single `SELECT`.

## Notes

- `ltm benchmark` generates synthetic events to exercise the store without
  recording; there is no simulated collector.
- The BPF object is compiled ahead of time and embedded
  (`internal/ebpf/collector_bpfel.o`); rebuild it with `make ebpf`.
