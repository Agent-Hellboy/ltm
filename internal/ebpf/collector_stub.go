//go:build !linux

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
