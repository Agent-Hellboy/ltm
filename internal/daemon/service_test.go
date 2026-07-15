package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ltm/internal/storage"
)

type oneEventSource struct {
	event storage.Event
}

func (s oneEventSource) Run(ctx context.Context, out chan<- storage.Event) error {
	select {
	case out <- s.event:
	case <-ctx.Done():
		return nil
	}
	<-ctx.Done()
	return nil
}

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// flushLoop should attribute the collector's dropped-event delta to the batch
// so the total surfaces in Status instead of vanishing.
func TestFlushLoopRecordsCollectorDrops(t *testing.T) {
	store := newTestStore(t)

	svc := NewService(store, Config{})
	ingest := make(chan storage.Event, 8)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	for i := range 3 {
		ingest <- storage.Event{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Category:  "process",
			Action:    "exec",
			PID:       i + 1,
		}
	}
	close(ingest)

	if err := svc.flushLoop(context.Background(), ingest, func() int64 { return 4 }); err != nil {
		t.Fatalf("flushLoop: %v", err)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != 3 {
		t.Fatalf("event count = %d, want 3", status.EventCount)
	}
	if status.DroppedEvents != 4 {
		t.Fatalf("dropped events = %d, want 4", status.DroppedEvents)
	}
}

// On shutdown the flush loop must drain events still buffered in the channel
// and persist them with a fresh context, not drop them because the loop's ctx
// was cancelled.
func TestFlushLoopDrainsOnShutdown(t *testing.T) {
	store := newTestStore(t)

	svc := NewService(store, Config{})
	ingest := make(chan storage.Event, 8)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	for i := range 5 {
		ingest <- storage.Event{Timestamp: base.Add(time.Duration(i) * time.Second), Category: "file", Action: "write", PID: i + 1}
	}

	// Flusher exits on ingest close, not on cancel alone. Cancelled ctx must
	// still allow the final write via finish()'s fresh timeout context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	close(ingest)
	if err := svc.flushLoop(ctx, ingest, func() int64 { return 0 }); err != nil {
		t.Fatalf("flushLoop: %v", err)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != 5 {
		t.Fatalf("persisted %d events on shutdown, want 5 (buffered events dropped)", status.EventCount)
	}
}

func TestFlushLoopDrainsReadyBurstBeforeFlush(t *testing.T) {
	store := newTestStore(t)

	svc := NewService(store, Config{BatchSize: 2, FlushPeriod: time.Hour})
	ingest := make(chan storage.Event, 8)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	for i := range 5 {
		ingest <- storage.Event{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Category:  "file",
			Action:    "write",
			PID:       i + 1,
		}
	}
	close(ingest)

	if err := svc.flushLoop(context.Background(), ingest, func() int64 { return 0 }); err != nil {
		t.Fatalf("flushLoop: %v", err)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != 5 {
		t.Fatalf("event count = %d, want 5", status.EventCount)
	}
}

func TestRunReturnsFlushError(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	svc := NewService(store, Config{BatchSize: 1, FlushPeriod: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = svc.runWithSource(ctx, oneEventSource{event: storage.Event{
		Timestamp: time.Now(),
		Category:  "file",
		Action:    "write",
		PID:       1,
	}})
	if err == nil || !strings.Contains(err.Error(), "store closed") {
		t.Fatalf("runWithSource error = %v, want flush/store error", err)
	}
}

// postCancelSource waits for cancellation, then tries to deliver one more event.
// With join-then-close shutdown, that late event must still be persisted.
type postCancelSource struct {
	event storage.Event
}

func (s postCancelSource) Run(ctx context.Context, out chan<- storage.Event) error {
	<-ctx.Done()
	select {
	case out <- s.event:
	default:
	}
	return nil
}

func TestRunPersistsEventSentAfterCancel(t *testing.T) {
	// Race-sensitive: exercise repeatedly so a premature drain/exit would flake.
	for attempt := 0; attempt < 40; attempt++ {
		store := newTestStore(t)
		svc := NewService(store, Config{BatchSize: 1, FlushPeriod: time.Hour})
		ctx, cancel := context.WithCancel(context.Background())

		base := time.Date(2026, 7, 8, 16, 0, 0, 0, time.UTC)
		ev := storage.Event{Timestamp: base, Category: "file", Action: "write", PID: attempt + 1}

		errCh := make(chan error, 1)
		go func() {
			errCh <- svc.runWithSource(ctx, postCancelSource{event: ev})
		}()

		time.Sleep(5 * time.Millisecond)
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("attempt %d: runWithSource: %v", attempt, err)
		}

		status, err := store.Status(context.Background())
		if err != nil {
			t.Fatalf("attempt %d: status: %v", attempt, err)
		}
		if status.EventCount != 1 {
			t.Fatalf("attempt %d: persisted %d events, want 1 late event after cancel", attempt, status.EventCount)
		}
	}
}
