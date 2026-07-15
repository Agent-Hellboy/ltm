package ebpf

import (
	"context"

	"ltm/internal/storage"
)

// EventSource is the daemon-facing control-plane entry: start recording until
// ctx is cancelled or an unrecoverable load/read error occurs.
type EventSource interface {
	Run(context.Context, chan<- storage.Event) error
}
