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
	BufferSize  int
	BatchSize   int
	FlushPeriod time.Duration
}

type Service struct {
	store *storage.Store
	cfg   Config
}

func NewService(store *storage.Store, cfg Config) *Service {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 2048
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 128
	}
	if cfg.FlushPeriod <= 0 {
		cfg.FlushPeriod = 1 * time.Second
	}
	return &Service{store: store, cfg: cfg}
}

func (s *Service) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ingest := make(chan storage.Event, s.cfg.BufferSize)
	col := collector.New(collector.Config{
		IgnorePaths: s.cfg.IgnorePaths,
		BufferSize:  s.cfg.BufferSize / 2,
	})

	sources := []ebpf.Source{ebpf.RealCollector{}}

	errCh := make(chan error, 2)
	go func() {
		errCh <- col.Run(ctx, sources, ingest)
	}()
	go func() {
		errCh <- s.flushLoop(ctx, ingest, func() int64 { return col.Stats().DroppedEvents })
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	}
}

// flushLoop batches incoming events and writes them to the store. droppedFn
// reports the collector's cumulative count of events dropped when the ingest
// channel was full; the delta since the last flush is recorded on the batch so
// gaps in the timeline stay visible instead of vanishing silently.
func (s *Service) flushLoop(ctx context.Context, ingest <-chan storage.Event, droppedFn func() int64) error {
	ticker := time.NewTicker(s.cfg.FlushPeriod)
	defer ticker.Stop()
	batch := make([]storage.Event, 0, s.cfg.BatchSize)
	var lastDropped int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if droppedFn != nil {
			if cur := droppedFn(); cur > lastDropped {
				batch[0].DroppedBefore += cur - lastDropped
				lastDropped = cur
			}
		}
		_, err := s.store.InsertEvents(ctx, batch)
		batch = batch[:0]
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return flush()
		case ev, ok := <-ingest:
			if !ok {
				return flush()
			}
			ev.SchemaVersion = storage.SchemaVersion
			batch = append(batch, ev)
			if len(batch) >= s.cfg.BatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}
