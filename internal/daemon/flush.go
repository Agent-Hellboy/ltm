package daemon

import (
	"context"
	"time"

	"ltm/internal/abi"
	"ltm/internal/storage"
)

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
