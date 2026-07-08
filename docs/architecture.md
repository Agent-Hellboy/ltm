# Architecture

The MVP is intentionally split around a narrow event contract.

## Flow

1. A collector emits normalized `storage.Event` values.
2. `collector` applies ignore rules and bounded buffering.
3. `daemon` batches writes and flushes to the store.
4. `storage` appends events and maintains read models for timeline, diff, and query.
5. `diff` and `query` work only from stored metadata.

## Phase 1

- Demo collector
- Append-only local store
- Deterministic diff and query engines
- CLI end to end

## Phase 2

- Real eBPF syscall tracepoint collector on Linux (`--mode ebpf`)
- Broad syscall coverage: process, file, memory, network, and block I/O tracepoints
- Embedded BPF object in `internal/ebpf/collector_bpfel.o`
- Same event schema and storage pipeline

