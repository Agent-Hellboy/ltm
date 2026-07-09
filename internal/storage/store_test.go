package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreInsertAndQuery(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
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
	path := filepath.Join(dir, "ltm.db")
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

func TestStoreQueryFilter(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: base, Category: "process", Action: "exec", PID: 100, UID: 0, Comm: "nginx", Exe: "/usr/sbin/nginx"},
		{Timestamp: base.Add(time.Minute), Category: "file", Action: "write", PID: 100, UID: 0, Comm: "nginx", Path: "/etc/nginx/nginx.conf"},
		{Timestamp: base.Add(2 * time.Minute), Category: "network", Action: "connect", PID: 200, UID: 1000, Comm: "curl", RemoteAddr: "127.0.0.1", RemotePort: 8080},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	byPID, err := store.Query(context.Background(), Filter{PIDs: []int{100}})
	if err != nil {
		t.Fatalf("query by pid: %v", err)
	}
	if len(byPID) != 2 {
		t.Fatalf("query by pid len = %d, want 2", len(byPID))
	}

	byUID, err := store.Query(context.Background(), Filter{UIDs: []int{1000}})
	if err != nil {
		t.Fatalf("query by uid: %v", err)
	}
	if len(byUID) != 1 || byUID[0].Comm != "curl" {
		t.Fatalf("query by uid = %+v, want single curl event", byUID)
	}

	byCategory, err := store.Query(context.Background(), Filter{Categories: []string{"file"}, PathLike: "/etc/%"})
	if err != nil {
		t.Fatalf("query by category+path: %v", err)
	}
	if len(byCategory) != 1 || byCategory[0].Path != "/etc/nginx/nginx.conf" {
		t.Fatalf("query by category+path = %+v, want single nginx.conf event", byCategory)
	}

	combined, err := store.Query(context.Background(), Filter{
		From: base, To: base.Add(90 * time.Second), Comms: []string{"nginx"}, Actions: []string{"write"},
	})
	if err != nil {
		t.Fatalf("combined query: %v", err)
	}
	if len(combined) != 1 || combined[0].Action != "write" {
		t.Fatalf("combined query = %+v, want single write event", combined)
	}

	none, err := store.Query(context.Background(), Filter{Comms: []string{"nonexistent"}})
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("empty query len = %d, want 0", len(none))
	}
}

func TestOpenReadOnlyRejectsMissingDB(t *testing.T) {
	t.Parallel()
	_, err := OpenReadOnly(filepath.Join(t.TempDir(), "does-not-exist.db"))
	if err == nil {
		t.Fatal("expected error opening missing db read-only")
	}
}

func TestOpenReadOnlyBlocksWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ltm.db")

	writer, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	if _, err := writer.InsertEvents(context.Background(), GenerateDemoEvents(base, 3)); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer reader.Close()

	if _, err := reader.InsertEvents(context.Background(), GenerateDemoEvents(base, 1)); err == nil {
		t.Fatal("expected InsertEvents to fail on read-only store")
	}

	if _, _, err := reader.RawSQL(context.Background(), "DELETE FROM events"); err == nil {
		t.Fatal("expected raw DELETE to fail on read-only connection")
	}

	cols, rows, err := reader.RawSQL(context.Background(), "SELECT count(*) FROM events")
	if err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if len(cols) != 1 || len(rows) != 1 {
		t.Fatalf("unexpected raw select shape: cols=%v rows=%v", cols, rows)
	}
}

func TestStorePrune(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: base, Category: "process", Action: "exec", PID: 1, Comm: "old"},
		{Timestamp: base.Add(48 * time.Hour), Category: "process", Action: "exec", PID: 2, Comm: "new"},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	n, err := store.Prune(context.Background(), base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.EventCount != 1 {
		t.Fatalf("event count after prune = %d, want 1", status.EventCount)
	}

	// Pruning again below the remaining event is a no-op and must not error.
	n, err = store.Prune(context.Background(), base)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if n != 0 {
		t.Fatalf("second prune removed %d, want 0", n)
	}
}

func TestStatusSumsDroppedEvents(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: base, Category: "process", Action: "exec", PID: 1, DroppedBefore: 3},
		{Timestamp: base.Add(time.Second), Category: "process", Action: "exec", PID: 2, DroppedBefore: 5},
	}
	stats, err := store.InsertEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if stats.Dropped != 8 {
		t.Fatalf("InsertStats.Dropped = %d, want 8 (sum of batch)", stats.Dropped)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.DroppedEvents != 8 {
		t.Fatalf("Status.DroppedEvents = %d, want 8 (total across log)", status.DroppedEvents)
	}
}

func TestEventsAfterIDAndLatest(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	if _, err := store.InsertEvents(context.Background(), GenerateDemoEvents(base, 10)); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	latest, err := store.LatestEventID(context.Background())
	if err != nil {
		t.Fatalf("latest id: %v", err)
	}
	if latest != 10 {
		t.Fatalf("LatestEventID = %d, want 10", latest)
	}

	// Nothing newer than the tail.
	tail, err := store.EventsAfterID(context.Background(), latest, 100)
	if err != nil {
		t.Fatalf("events after tail: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("EventsAfterID(latest) = %d events, want 0", len(tail))
	}

	// Everything after id 6, ascending and contiguous.
	after, err := store.EventsAfterID(context.Background(), 6, 100)
	if err != nil {
		t.Fatalf("events after 6: %v", err)
	}
	if len(after) != 4 {
		t.Fatalf("EventsAfterID(6) = %d events, want 4", len(after))
	}
	for i, ev := range after {
		if ev.ID != int64(7+i) {
			t.Fatalf("EventsAfterID(6)[%d].ID = %d, want %d", i, ev.ID, 7+i)
		}
	}
}
