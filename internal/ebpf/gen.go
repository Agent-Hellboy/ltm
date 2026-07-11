package ebpf

// Code generation for the capture collector.
//
// bpf2go compiles collector.bpf.c and, from the resulting object's BTF, emits
// both the embedded object (collector_bpfel.o) and its Go bindings
// (collector_bpfel.go) — including the collectorEvent struct. The layout
// originates in abi.yaml, which generates ../abi/kernel_event.gen.h (included
// by collector.bpf.c); collectorEvent is derived from that object's BTF, so it
// can never drift from the source of truth.
//
// Requires clang with a working bpf target (the Linux build box / CI); Apple
// clang cannot target bpf. Run via `make ebpf`, then commit the regenerated
// collector_bpfel.{o,go}.
//
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -target bpfel -cflags "-O2 -g -mllvm -bpf-stack-size=1024 -D__TARGET_ARCH_x86 -I./headers" collector collector.bpf.c
