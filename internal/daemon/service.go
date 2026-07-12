package daemon

import (
	"context"
	"time"

	"ltm/internal/collector"
	"ltm/internal/ebpf"
	"ltm/internal/storage"
)

type Config struct {
	IgnorePaths []string
	BatchSize   int
	FlushPeriod time.Duration
}

type Service struct {
	store *storage.Store
	cfg   Config
}

func NewService(store *storage.Store, cfg Config) *Service {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 128
	}
	if cfg.FlushPeriod <= 0 {
		cfg.FlushPeriod = 1 * time.Second
	}
	return &Service{store: store, cfg: cfg}
}

func (s *Service) Run(ctx context.Context) error {
	return s.runWithSource(ctx, ebpf.NewSource())
}

func (s *Service) runWithSource(ctx context.Context, src ebpf.EventSource) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ingest := make(chan storage.Event, 2048)
	col := collector.New(collector.Config{
		IgnorePaths: s.cfg.IgnorePaths,
		BufferSize:  1024,
	})

	colErr := make(chan error, 1)
	go func() {
		colErr <- col.Run(ctx, src, ingest)
	}()
	flushErr := make(chan error, 1)
	go func() {
		flushErr <- s.flushLoop(ctx, ingest, col.DroppedEvents)
	}()

	var runErr error
	flushDone := false
	select {
	case <-ctx.Done():
	case runErr = <-colErr:
	case runErr = <-flushErr:
		flushDone = true
	}
	// Stop the collector and flush loop, then wait for the flush loop to drain
	// and persist its final batch before returning. The caller closes the store
	// right after Run returns, so returning early would drop buffered events and
	// race the writer against Store.Close.
	cancel()
	if flushDone {
		if cerr := <-colErr; cerr != nil && runErr == nil {
			runErr = cerr
		}
	} else {
		if ferr := <-flushErr; ferr != nil && runErr == nil {
			runErr = ferr
		}
	}
	return runErr
}
