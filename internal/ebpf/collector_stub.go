//go:build !linux

// Recording (eBPF) is Linux-only, but cmd/ltm still imports this package on
// every OS via daemon → ebpf. This stub keeps go build working off Linux
// (query, timeline, benchmark, local tests) by providing NewSource/Run that
// return ErrNotImplemented instead of compiling the real control plane.
package ebpf

import (
	"context"

	"ltm/internal/storage"
)

type collector struct{}

func NewSource() EventSource { return collector{} }

func (collector) Run(context.Context, chan<- storage.Event) error {
	return ErrNotImplemented
}

var ErrNotImplemented = &collectorError{"eBPF collector not implemented on this platform"}

type collectorError struct{ msg string }

func (e *collectorError) Error() string { return e.msg }
