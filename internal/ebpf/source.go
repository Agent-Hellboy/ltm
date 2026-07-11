package ebpf

import (
	"context"

	"ltm/internal/storage"
)

// EventSource emits storage events from some backing capture mechanism.
type EventSource interface {
	Run(context.Context, chan<- storage.Event) error
}
