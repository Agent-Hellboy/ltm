# ABI

This folder is the stable reference for contracts that other parts of `ltm`
consume:

- the persisted SQLite event schema
- the kernel-to-userspace capture transport contract
- field meanings and invariants
- compatibility rules for schema changes

Handwritten inputs live in `internal/abi/abi.yaml` and `internal/ebpf/collector.bpf.c`.
Generated files such as `kernel_event.gen.h`, `tracepoints_gen.go`, and
`collector_bpfel.go` are derived artifacts and should not be edited directly.

Pages:

| Page | Contents |
|---|---|
| [capture.md](capture.md) | Embedded BPF object contract, loader expectations, maps/programs, compatibility rules |
| [schema.md](schema.md) | `events` table reference, per-column semantics, metadata conventions, compatibility rules |

Related:

- [Querying](../querying.md) for CLI examples and ad-hoc SQL usage
- [Architecture](../architecture.md) for the event pipeline
- [Recording](../recording.md) for capture-side limitations
