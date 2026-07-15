package daemon

import (
	"context"
	"time"

	"ltm/internal/storage"
)

// flushLoop is the pipeline's chunk/flush stage. It wraps eventBatcher so tests
// can keep calling svc.flushLoop. droppedFn reports the collector's cumulative
// count of events dropped when the ingest channel was full; the delta since the
// last flush is recorded on the batch so gaps in the timeline stay visible.
//
// Lifecycle: keeps reading until ingest is closed. Cancellation alone does not
// finish the stage — the service must join the producer and close(ingest).
func (s *Service) flushLoop(ctx context.Context, ingest <-chan storage.Event, droppedFn func() int64) error {
	b := &eventBatcher{
		store:       s.store,
		batchSize:   s.cfg.BatchSize,
		maxBatch:    defaultMaxFlushBatchSize,
		flushPeriod: s.cfg.FlushPeriod,
		droppedFn:   droppedFn,
		batch:       make([]storage.Event, 0, s.cfg.BatchSize),
	}
	return b.run(ctx, ingest)
}

// eventBatcher holds the in-flight chunk for the flush stage. All fields are
// lexically confined to the single flush goroutine (no mutex). Pattern map:
// for-select loop, chunking before InsertEvents, exit on channel close.
type eventBatcher struct {
	store       *storage.Store
	batchSize   int
	maxBatch    int
	flushPeriod time.Duration
	droppedFn   func() int64
	batch       []storage.Event
	lastDropped int64
}

func (b *eventBatcher) append(ev storage.Event) {
	b.batch = append(b.batch, ev)
}

func (b *eventBatcher) flush(ctx context.Context) error {
	if len(b.batch) == 0 {
		return nil
	}
	if cur := b.droppedFn(); cur > b.lastDropped {
		b.batch[0].DroppedBefore += cur - b.lastDropped
		b.lastDropped = cur
	}
	_, err := b.store.InsertEvents(ctx, b.batch)
	if err != nil {
		return err
	}
	b.batch = b.batch[:0]
	return nil
}

// writeCtx returns ctx while it is live; after cancel, use a short timeout so
// mid-shutdown size flushes still persist instead of failing with Canceled.
func (b *eventBatcher) writeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// drainReady non-blocking-pulls from ingest until empty or maxBatch. Returns
// false when ingest is closed (caller should finish).
func (b *eventBatcher) drainReady(ingest <-chan storage.Event) bool {
	for len(b.batch) < b.maxBatch {
		select {
		case ev, ok := <-ingest:
			if !ok {
				return false
			}
			b.append(ev)
		default:
			return true
		}
	}
	return true
}

// finish persists any leftover batch with a fresh context so a cancelled run
// ctx cannot abort the final write.
func (b *eventBatcher) finish() error {
	fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer fcancel()
	return b.flush(fctx)
}

// run is the for-select consumer: ingest (chunk on size) or ticker (time flush).
// It does not exit on ctx.Done alone — that would race an active producer.
func (b *eventBatcher) run(ctx context.Context, ingest <-chan storage.Event) error {
	ticker := time.NewTicker(b.flushPeriod)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-ingest:
			if !ok {
				return b.finish()
			}
			b.append(ev)
			if len(b.batch) >= b.batchSize {
				if !b.drainReady(ingest) {
					return b.finish()
				}
				wctx, wcancel := b.writeCtx(ctx)
				err := b.flush(wctx)
				wcancel()
				if err != nil {
					return err
				}
			}
		case <-ticker.C:
			if ctx.Err() != nil {
				// Wait for ingest close after the producer joins.
				continue
			}
			if err := b.flush(ctx); err != nil {
				return err
			}
		}
	}
}
