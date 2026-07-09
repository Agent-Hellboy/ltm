package query

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ltm/internal/storage"
)

func TestQueryEngine(t *testing.T) {
	t.Parallel()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

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
}

