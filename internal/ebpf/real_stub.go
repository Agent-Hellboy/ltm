//go:build !linux

package ebpf

import (
	"context"

	"ltm/internal/storage"
)

type RealCollector struct{}

func (RealCollector) Name() string { return "ebpf" }

func (RealCollector) Run(context.Context, chan<- storage.Event) error {
	return ErrNotImplemented
}

var ErrNotImplemented = &collectorError{"real eBPF collector not implemented on this platform"}

type collectorError struct{ msg string }

func (e *collectorError) Error() string { return e.msg }
