// Package ebpf is the userspace control plane for ltm's kernel recorder.
//
// The in-kernel “data plane” is the BPF bytecode built from collector.bpf.c
// (submitted events land in the events ring buffer). This package does not
// implement that logic in Go; it loads, wires, and drains it:
//
//  1. Parse the embedded ELF (collector_bpfel.o) into a collection spec.
//  2. Create maps and load programs into the kernel (cilium/ebpf).
//  3. Attach programs to tracepoints (attach_linux.go).
//  4. Read committed ring-buffer records and map them to storage.Event.
//
// EventSource is the daemon-facing contract. On Linux, NewSource returns the
// real loader/reader; on other OSes the stub returns ErrNotImplemented so the
// rest of the CLI still builds. Rebuild the embedded object with `make ebpf`
// after changing collector.bpf.c or the ABI layout.
package ebpf

import (
	"context"

	"ltm/internal/storage"
)

// EventSource emits storage events from some backing capture mechanism.
type EventSource interface {
	Run(context.Context, chan<- storage.Event) error
}
