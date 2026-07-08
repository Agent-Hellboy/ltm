package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreInsertAndQuery(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.log"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := GenerateDemoEvents(base, 12)
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != int64(len(events)) {
		t.Fatalf("event count = %d, want %d", status.EventCount, len(events))
	}

	timeline, err := store.EventsBetween(context.Background(), base.Add(-time.Minute), base.Add(20*time.Minute), 100)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(timeline) != len(events) {
		t.Fatalf("timeline len = %d, want %d", len(timeline), len(events))
	}

	fileEvents, err := store.EventsByPath(context.Background(), "/tmp/ltm-demo.txt", 100)
	if err != nil {
		t.Fatalf("events by path: %v", err)
	}
	if len(fileEvents) == 0 {
		t.Fatalf("expected file events")
	}

	pidEvents, err := store.EventsByPID(context.Background(), 4300, 100)
	if err != nil {
		t.Fatalf("events by pid: %v", err)
	}
	if len(pidEvents) == 0 {
		t.Fatalf("expected pid events")
	}
}

func TestStoreReloadPopulatesTimeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ltm.log")
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := GenerateDemoEvents(base, 8)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reloaded.Close()

	status, err := reloaded.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != int64(len(events)) {
		t.Fatalf("event count = %d, want %d", status.EventCount, len(events))
	}

	timeline, err := reloaded.EventsBetween(context.Background(), base.Add(-time.Minute), base.Add(20*time.Minute), 100)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(timeline) != len(events) {
		t.Fatalf("timeline len = %d, want %d", len(timeline), len(events))
	}
}
