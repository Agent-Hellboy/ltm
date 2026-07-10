package query

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ltm/internal/storage"
)

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestQueryEngine(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []storage.Event{
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base,
			Category:      "file",
			Action:        "write",
			PID:           1234,
			PPID:          1,
			UID:           0,
			Comm:          "vim",
			Path:          "/etc/nginx/nginx.conf",
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(1 * time.Minute),
			Category:      "network",
			Action:        "listen",
			PID:           2222,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			LocalAddr:     "0.0.0.0",
			LocalPort:     8080,
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(2 * time.Minute),
			Category:      "network",
			Action:        "connect",
			PID:           3333,
			PPID:          1,
			UID:           1000,
			Comm:          "curl",
			RemoteAddr:    "127.0.0.1",
			RemotePort:    8080,
			RemoteHost:    "localhost",
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(3 * time.Minute),
			Category:      "network",
			Action:        "connect",
			PID:           4444,
			PPID:          1,
			UID:           1000,
			Comm:          "local-only",
			LocalAddr:     "127.0.0.1",
			LocalPort:     39000,
			RemoteAddr:    "203.0.113.10",
			RemotePort:    443,
		},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	engine := NewEngine(store)
	res, err := engine.Execute(context.Background(), "who modified /etc/nginx/nginx.conf?")
	if err != nil {
		t.Fatalf("execute who modified: %v", err)
	}
	if len(res.Rows) == 0 || !strings.Contains(res.Rows[0], "vim") {
		t.Fatalf("unexpected who-modified result: %+v", res.Rows)
	}

	res, err = engine.Execute(context.Background(), "what process opened port 8080?")
	if err != nil {
		t.Fatalf("execute opened port: %v", err)
	}
	if len(res.Rows) == 0 || !strings.Contains(res.Rows[0], "nginx") {
		t.Fatalf("unexpected port result: %+v", res.Rows)
	}

	res, err = engine.Execute(context.Background(), "show activity for pid 1234")
	if err != nil {
		t.Fatalf("execute pid: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatalf("expected pid rows")
	}

	// Connection query by IP must find the curl -> 127.0.0.1 event.
	res, err = engine.Execute(context.Background(), "who connected to 127.0.0.1?")
	if err != nil {
		t.Fatalf("execute connected-to: %v", err)
	}
	if len(res.Rows) == 0 || !strings.Contains(res.Rows[0], "curl") {
		t.Fatalf("expected curl connection to 127.0.0.1, got: %+v", res.Rows)
	}
	for _, row := range res.Rows {
		if strings.Contains(row, "local-only") {
			t.Fatalf("connected-to IP query matched local_addr instead of remote target: %+v", res.Rows)
		}
	}

	res, err = engine.Execute(context.Background(), "who modified /etc/nginx/nginx.conf")
	if err != nil {
		t.Fatalf("execute who modified without question mark: %v", err)
	}
	if len(res.Rows) == 0 || !strings.Contains(res.Rows[0], "vim") {
		t.Fatalf("unexpected who-modified result without question mark: %+v", res.Rows)
	}
}

func TestQueryRestartAndConnection(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	events := []storage.Event{
		{Timestamp: base, Category: "process", Action: "exit", PID: 10, Comm: "nginx"},
		{Timestamp: base.Add(time.Second), Category: "process", Action: "exec", PID: 11, Comm: "nginx", Exe: "/usr/sbin/nginx"},
		{Timestamp: base.Add(2 * time.Second), Category: "process", Action: "exec", PID: 12, Comm: "redis", Exe: "/usr/bin/redis"},
		{Timestamp: base.Add(3 * time.Second), Category: "network", Action: "connect", PID: 13, Comm: "curl", RemoteAddr: "93.184.216.34", RemotePort: 80, RemoteHost: "example.com"},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	engine := NewEngine(store)

	// Restart query narrowed to nginx must return only nginx process events.
	res, err := engine.Execute(context.Background(), "what changed before nginx restarted?")
	if err != nil {
		t.Fatalf("restart query: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatalf("restart query returned no rows (regression: question words fed as filters)")
	}
	for _, row := range res.Rows {
		if !strings.Contains(row, "nginx") {
			t.Fatalf("restart query leaked non-nginx row: %q", row)
		}
	}

	// Connection query by hostname must match on remote_host.
	res, err = engine.Execute(context.Background(), "who connected to example.com?")
	if err != nil {
		t.Fatalf("connection-by-host query: %v", err)
	}
	if len(res.Rows) != 1 || !strings.Contains(res.Rows[0], "curl") {
		t.Fatalf("expected single curl connection to example.com, got: %+v", res.Rows)
	}
}
