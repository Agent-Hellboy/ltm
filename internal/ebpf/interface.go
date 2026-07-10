package ebpf

import (
	"context"

	"ltm/internal/storage"
)

type Source interface {
	Run(context.Context, chan<- storage.Event) error
}
