package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreInsertAndQuery(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: base, Category: "process", Action: "exec", PID: 4300, Comm: "worker", Exe: "/usr/bin/worker"},
		{Timestamp: base.Add(time.Minute), Category: "file", Action: "write", PID: 4300, Comm: "worker", Path: "/tmp/ltm-probe.txt"},
		{Timestamp: base.Add(2 * time.Minute), Category: "network", Action: "connect", PID: 4301, Comm: "curl", RemoteAddr: "127.0.0.1", RemotePort: 80},
	}
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

	fileEvents, err := store.EventsByPath(context.Background(), "/tmp/ltm-probe.txt", 100)
	if err != nil {
		t.Fatalf("events by path: %v", err)
	}
	if len(fileEvents) != 1 || fileEvents[0].PID != 4300 || fileEvents[0].Action != "write" {
		t.Fatalf("EventsByPath = %+v, want one write by pid 4300", fileEvents)
	}

	pidEvents, err := store.EventsByPID(context.Background(), 4300, 100)
	if err != nil {
		t.Fatalf("events by pid: %v", err)
	}
	if len(pidEvents) != 2 {
		t.Fatalf("EventsByPID(4300) = %d events, want 2", len(pidEvents))
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

func TestQueryTextEscapesLikeWildcards(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	if _, err := store.InsertEvents(context.Background(), []Event{
		{Timestamp: base, Category: "process", Action: "exec", PID: 1, Comm: "a_c"},
		{Timestamp: base.Add(time.Second), Category: "process", Action: "exec", PID: 2, Comm: "abc"},
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	// "_" is a LIKE wildcard; escaped, "a_c" must match only the literal "a_c",
	// not "abc".
	got, err := store.QueryText(context.Background(), []string{"a_c"}, 100)
	if err != nil {
		t.Fatalf("query text: %v", err)
	}
	if len(got) != 1 || got[0].Comm != "a_c" {
		t.Fatalf("QueryText(\"a_c\") = %d rows %+v, want only the literal a_c", len(got), got)
	}
}

func TestStoreQueryFilter(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

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
	if _, _, err := writer.RawSQL(context.Background(), "SELECT count(*) FROM events"); err == nil {
		t.Fatal("expected RawSQL to reject writable store")
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

func TestOpenEscapesURICharactersInPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ltm?weird#name%.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store with URI characters: %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at literal path %q: %v", path, err)
	}
}

func TestStorePrune(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

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

func TestGenerateDemoEventsCount(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	if got := GenerateDemoEvents(base, 0); len(got) != 0 {
		t.Fatalf("GenerateDemoEvents count 0 produced %d events, want 0", len(got))
	}
	if got := GenerateDemoEvents(base, -1); len(got) != 0 {
		t.Fatalf("GenerateDemoEvents negative count produced %d events, want 0", len(got))
	}
	if got := GenerateDemoEvents(base, 3); len(got) != 3 {
		t.Fatalf("GenerateDemoEvents count 3 produced %d events, want 3", len(got))
	}
}

func TestStatusSumsDroppedEvents(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

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
	store := newTestStore(t)

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

func TestStoreSockets(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: base, Category: "network", Action: "listen", PID: 10, Comm: "nginx", LocalAddr: "0.0.0.0", LocalPort: 8080},
		{Timestamp: base.Add(time.Second), Category: "network", Action: "bind", PID: 11, Comm: "sshd", LocalAddr: "0.0.0.0", LocalPort: 22},
		{Timestamp: base.Add(2 * time.Second), Category: "network", Action: "connect", PID: 12, Comm: "curl", RemoteAddr: "1.1.1.1", RemotePort: 443},
		{Timestamp: base.Add(3 * time.Second), Category: "file", Action: "write", PID: 13, Comm: "vim", Path: "/tmp/x"},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := store.Sockets(context.Background(), 100)
	if err != nil {
		t.Fatalf("Sockets: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Sockets len = %d, want 3 network rows", len(got))
	}
	byPort := map[int]SocketRecord{}
	for _, sr := range got {
		byPort[sr.LocalPort] = sr
	}
	if byPort[8080].Comm != "nginx" || byPort[8080].State != "listen" {
		t.Fatalf("port 8080 = %+v, want nginx listen", byPort[8080])
	}
	if byPort[22].Comm != "sshd" || byPort[22].State != "bind" {
		t.Fatalf("port 22 = %+v, want sshd bind", byPort[22])
	}
}
