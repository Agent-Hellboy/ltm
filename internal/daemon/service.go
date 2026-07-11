package daemon

import (
	"context"
	"time"

	"ltm/internal/abi"
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
	return s.runWithSource(ctx, ebpf.RealCollector{})
}

func (s *Service) runWithSource(ctx context.Context, src ebpf.Source) error {
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

// flushLoop batches incoming events and writes them to the store. droppedFn
// reports the collector's cumulative count of events dropped when the ingest
// channel was full; the delta since the last flush is recorded on the batch so
// gaps in the timeline stay visible instead of vanishing silently.
func (s *Service) flushLoop(ctx context.Context, ingest <-chan storage.Event, droppedFn func() int64) error {
	ticker := time.NewTicker(s.cfg.FlushPeriod)
	defer ticker.Stop()
	batch := make([]storage.Event, 0, s.cfg.BatchSize)
	var lastDropped int64
	flush := func(fctx context.Context) error {
		if len(batch) == 0 {
			return nil
		}
		if cur := droppedFn(); cur > lastDropped {
			batch[0].DroppedBefore += cur - lastDropped
			lastDropped = cur
		}
		_, err := s.store.InsertEvents(fctx, batch)
		batch = batch[:0]
		return err
	}
	for {
		select {
		case <-ctx.Done():
			// Drain anything still buffered, then persist with a fresh context
			// so the cancelled ctx doesn't abort the final write.
			for drained := false; !drained; {
				select {
				case ev, ok := <-ingest:
					if !ok {
						drained = true
					} else {
						batch = append(batch, ev)
					}
				default:
					drained = true
				}
			}
			fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer fcancel()
			return flush(fctx)
		case ev, ok := <-ingest:
			if !ok {
				return flush(ctx)
			}
			ev.SchemaVersion = abi.SchemaVersion
			batch = append(batch, ev)
			if len(batch) >= s.cfg.BatchSize {
				if err := flush(ctx); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(ctx); err != nil {
				return err
			}
		}
	}
}
