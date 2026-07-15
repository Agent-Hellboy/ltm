package daemon

import (
	"context"
	"time"

	"ltm/internal/storage"
)

const flushWriteTimeout = 5 * time.Second

// flushLoop is the pipeline's chunk/flush stage. It wraps eventBatcher so tests
// can keep calling svc.flushLoop. droppedFn reports the collector's cumulative
// count of events dropped when the ingest channel was full; the delta since the
// last flush is recorded on the batch so gaps in the timeline stay visible.
//
// Lifecycle: keeps reading until ingest is closed. Cancellation alone does not
// finish the stage — the service must join the producer and close(ingest).
// InsertEvents never uses the run ctx, so cancel cannot abort a flush (TOCTOU)
// and cause an early return that leaves ingest unread.
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

// writeCtx always returns Background+timeout. The run ctx must not cancel DB
// writes: a TOCTOU race (check live, then cancel, then InsertEvents) would make
// run() return early before ingest is closed and lose queued events.
func (b *eventBatcher) writeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), flushWriteTimeout)
}

func (b *eventBatcher) flushWrite() error {
	ctx, cancel := b.writeCtx()
	defer cancel()
	return b.flush(ctx)
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

func (b *eventBatcher) finish() error {
	return b.flushWrite()
}

// run is the for-select consumer: ingest (chunk on size) or ticker (time flush).
// It does not exit on ctx.Done alone — that would race an active producer.
// ctx is only used to skip pointless ticker work after cancel while waiting for
// ingest close; every InsertEvents call uses writeCtx().
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
				if err := b.flushWrite(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if ctx.Err() != nil {
				// Wait for ingest close after the producer joins; size path and
				// finish still persist with writeCtx.
				continue
			}
			if err := b.flushWrite(); err != nil {
				return err
			}
		}
	}
}
