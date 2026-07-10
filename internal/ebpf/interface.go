package ebpf

import (
	"context"

	"ltm/internal/storage"
)

type Source interface {
	Name() string
	Run(context.Context, chan<- storage.Event) error
}
