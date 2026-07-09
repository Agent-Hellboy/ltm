package diff

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ltm/internal/storage"
)

func TestDiffEngine(t *testing.T) {
	t.Parallel()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	events := []storage.Event{
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(1 * time.Minute),
			Category:      "process",
			Action:        "exec",
			PID:           1001,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			Exe:           "/usr/sbin/nginx",
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(2 * time.Minute),
			Category:      "file",
			Action:        "write",
			PID:           1001,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			Path:          "/etc/nginx/nginx.conf",
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(3 * time.Minute),
			Category:      "network",
			Action:        "listen",
			PID:           1001,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			LocalAddr:     "0.0.0.0",
			LocalPort:     8080,
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(4 * time.Minute),
			Category:      "network",
			Action:        "connect",
			PID:           1002,
			PPID:          1,
			UID:           1000,
			Comm:          "curl",
			RemoteAddr:    "127.0.0.1",
			RemotePort:    8080,
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(5 * time.Minute),
			Category:      "process",
			Action:        "exit",
			PID:           1001,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			ExitCode:      0,
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(6 * time.Minute),
			Category:      "process",
			Action:        "exec",
			PID:           1001,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			Exe:           "/usr/sbin/nginx",
		},
	}

	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	report, err := NewEngine(store).Diff(context.Background(), base, base.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	if len(report.NewProcesses) == 0 {
		t.Fatalf("expected new processes")
	}
	if len(report.ModifiedFiles) == 0 {
		t.Fatalf("expected modified files")
	}
	if len(report.NewListeners) == 0 {
		t.Fatalf("expected new listeners")
	}
	if len(report.OutboundConnections) == 0 {
		t.Fatalf("expected outbound connections")
	}
	if len(report.Restarts) == 0 {
		t.Fatalf("expected restart detection")
	}
}

