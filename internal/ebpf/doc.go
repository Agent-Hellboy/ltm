// Package ebpf is the userspace control plane for ltm recording.
//
// # Data plane vs control plane
//
// Data plane (in-kernel): collector.bpf.c is compiled to BPF bytecode. Tracepoint
// programs reserve/submit records into the events BPF_MAP_TYPE_RINGBUF. That
// work runs inside the kernel when hooks fire; Go does not execute it.
//
// Control plane (this package, via cilium/ebpf): load that bytecode and maps,
// attach programs to hooks, and read committed ring-buffer records into
// storage.Event for the daemon pipeline (collector → ingest → flush → SQLite).
//
// # Kernel ↔ userspace interaction
//
// The control plane is how userspace and kernel space meet for recording.
// There is no custom kernel module and no copying event structs through ordinary
// read()/write() syscalls from BPF; interaction goes through the BPF subsystem:
//
//   Setup (userspace → kernel), once at start:
//     Go/cilium issues bpf() syscalls to create maps, load bytecode, and attach
//     programs to kernel tracepoints. After that, the programs live in the kernel.
//
//   Steady state (kernel → userspace), per event:
//     A tracepoint fires → BPF data plane fills a slot in the events ring buffer
//     (bpf_ringbuf_reserve / submit; those are in-kernel helpers, not Go calls).
//     The ring buffer is kernel-managed memory; userspace observes it via the map
//     FD. ringbuf.Reader.Read wakes when records are committed and returns bytes
//     that decode_linux.go turns into storage.Event.
//
// So: control plane wires the pipes; data plane produces into the ring buffer;
// control plane drains that buffer into the rest of ltm.
//
// # How loading works
//
// clang/bpf2go produce collector_bpfel.o (ELF) and collector_bpfel.go. The .o is
// embedded in the ltm binary. At start this package:
//
//  1. Parses the ELF into a CollectionSpec (programs, maps, BTF).
//  2. Creates maps in the kernel (e.g. events ring buffer) — BPF_MAP_CREATE.
//  3. Loads each program's bytecode — BPF_PROG_LOAD (verifier accepts it).
//  4. Attaches programs to tracepoints (attach_linux.go / link.Tracepoint).
//  5. Opens a ringbuf.Reader on events and maps RawSample → storage.Event.
//
// bpf_* helpers in the C program are kernel helpers. cilium/ebpf drives the
// userspace bpf() syscalls for load/attach/read.
//
// # Layout
//
//   - doc.go               — package overview (control vs data plane)
//   - source.go            — EventSource contract
//   - collector.bpf.c      — in-kernel programs + maps (data plane source)
//   - collector_bpfel.*    — generated embedded object + Go bindings
//   - collector_linux.go   — Linux Run: load, attach, read loop
//   - attach_linux.go      — tracepoint attachment
//   - decode_linux.go      — kernel record → storage.Event
//   - proc_linux.go        — boot-time helpers for timestamps
//   - collector_stub.go    — !linux stub so CLI still builds
//
// Rebuild the embedded object with `make ebpf` after changing collector.bpf.c
// or the ABI layout (abi.yaml → kernel_event.gen.h).
package ebpf
