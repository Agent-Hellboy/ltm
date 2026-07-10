package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ltm/internal/storage"
)

// flushLoop should attribute the collector's dropped-event delta to the batch
// so the total surfaces in Status instead of vanishing.
func TestFlushLoopRecordsCollectorDrops(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

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

	// Channel is closed, so flushLoop drains, flushes, and returns.
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
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc := NewService(store, Config{})
	ingest := make(chan storage.Event, 8)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	for i := range 5 {
		ingest <- storage.Event{Timestamp: base.Add(time.Duration(i) * time.Second), Category: "file", Action: "write", PID: i + 1}
	}

	// Cancel first: the events sit unread in the buffer when flushLoop starts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.flushLoop(ctx, ingest, nil); err != nil {
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

// A nil dropped reporter must be handled gracefully (no attribution, no panic).
func TestFlushLoopNilDroppedFn(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc := NewService(store, Config{})
	ingest := make(chan storage.Event, 4)
	ingest <- storage.Event{Timestamp: time.Now(), Category: "file", Action: "write", PID: 1}
	close(ingest)

	if err := svc.flushLoop(context.Background(), ingest, nil); err != nil {
		t.Fatalf("flushLoop: %v", err)
	}
	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != 1 || status.DroppedEvents != 0 {
		t.Fatalf("status = %+v, want 1 event / 0 dropped", status)
	}
}
