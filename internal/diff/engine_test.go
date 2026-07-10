package diff

import (
	"context"
	"path/filepath"
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

func TestDiffEngine(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

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
			PID:           1004,
			PPID:          1,
			UID:           0,
			Comm:          "nginx",
			Exe:           "/usr/sbin/nginx",
		},
		{
			SchemaVersion: storage.SchemaVersion,
			Timestamp:     base.Add(7 * time.Minute),
			Category:      "file",
			Action:        "rmdir",
			PID:           1003,
			PPID:          1,
			UID:           0,
			Comm:          "rmdir",
			Path:          "/var/run/nginx",
		},
	}

	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	report, err := NewEngine(store).Diff(context.Background(), base, base.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	if len(report.NewProcesses) != 2 {
		t.Fatalf("NewProcesses = %+v, want 2 (pids 1001 and 1004)", report.NewProcesses)
	}
	if report.NewProcesses[0].PID != 1001 || report.NewProcesses[0].Comm != "nginx" {
		t.Fatalf("NewProcesses[0] = %+v, want pid=1001 comm=nginx", report.NewProcesses[0])
	}
	if report.NewProcesses[1].PID != 1004 || report.NewProcesses[1].Comm != "nginx" {
		t.Fatalf("NewProcesses[1] = %+v, want pid=1004 comm=nginx", report.NewProcesses[1])
	}

	if len(report.ExitedProcesses) != 1 || report.ExitedProcesses[0].PID != 1001 {
		t.Fatalf("ExitedProcesses = %+v, want pid 1001", report.ExitedProcesses)
	}

	if len(report.ModifiedFiles) != 1 || report.ModifiedFiles[0].Path != "/etc/nginx/nginx.conf" {
		t.Fatalf("ModifiedFiles = %+v, want /etc/nginx/nginx.conf", report.ModifiedFiles)
	}

	if len(report.NewListeners) != 1 ||
		report.NewListeners[0].PID != 1001 ||
		report.NewListeners[0].Socket != "0.0.0.0:8080" {
		t.Fatalf("NewListeners = %+v, want nginx 0.0.0.0:8080", report.NewListeners)
	}

	if len(report.OutboundConnections) != 1 ||
		report.OutboundConnections[0].Comm != "curl" ||
		report.OutboundConnections[0].Socket != "127.0.0.1:8080" {
		t.Fatalf("OutboundConnections = %+v, want curl 127.0.0.1:8080", report.OutboundConnections)
	}

	if len(report.Restarts) != 1 || report.Restarts[0].PID != 1004 || report.Restarts[0].Comm != "nginx" {
		t.Fatalf("Restarts = %+v, want nginx pid 1004", report.Restarts)
	}

	if len(report.DeletedFiles) != 1 || report.DeletedFiles[0].Path != "/var/run/nginx" {
		t.Fatalf("DeletedFiles = %+v, want /var/run/nginx", report.DeletedFiles)
	}
}
