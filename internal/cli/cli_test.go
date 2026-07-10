package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ltm/internal/storage"
)

func TestParseDurationOrTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	if got, err := parseDurationOrTime("now", now); err != nil || !got.Equal(now) {
		t.Fatalf("parseDurationOrTime(now) = %v, %v; want %v", got, err, now)
	}
	if got, err := parseDurationOrTime("1h", now); err != nil || !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("parseDurationOrTime(1h) = %v, %v; want %v", got, err, now.Add(-time.Hour))
	}
	if _, err := parseDurationOrTime("2026-07-08 14:00", now); err != nil {
		t.Fatalf("parseDurationOrTime(absolute): %v", err)
	}
	if _, err := parseDurationOrTime("not-a-time", now); err == nil {
		t.Fatal("expected error for unparseable time")
	}
}

func TestWatchPredicate(t *testing.T) {
	t.Parallel()
	ev := storage.Event{Category: "file", Comm: "nginx", PID: 100}

	// No filters: everything matches.
	if !watchPredicate(nil, nil, nil)(ev) {
		t.Fatal("empty predicate should match")
	}
	// Matching category, comm, pid.
	if !watchPredicate([]string{"file"}, []string{"nginx"}, []int{100})(ev) {
		t.Fatal("expected match on all filters")
	}
	// Non-matching category filters it out.
	if watchPredicate([]string{"network"}, nil, nil)(ev) {
		t.Fatal("category mismatch should not match")
	}
	// Non-matching pid filters it out even if category matches.
	if watchPredicate([]string{"file"}, nil, []int{999})(ev) {
		t.Fatal("pid mismatch should not match")
	}
}

func TestWatchStep(t *testing.T) {
	t.Parallel()
	store, err := storage.Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	if _, err := store.InsertEvents(context.Background(), storage.GenerateDemoEvents(base, 12)); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	var buf bytes.Buffer
	matchNetwork := watchPredicate([]string{"network"}, nil, nil)
	newID, err := watchStep(context.Background(), store, 0, 100, matchNetwork, &buf)
	if err != nil {
		t.Fatalf("watchStep: %v", err)
	}
	if newID != 12 {
		t.Fatalf("watchStep advanced to id %d, want 12 (all events consumed)", newID)
	}
	out := buf.String()
	if !strings.Contains(out, "network") || strings.Contains(out, "process") {
		t.Fatalf("watchStep output should contain only network events:\n%s", out)
	}

	// A second step from the high-water mark yields nothing new.
	buf.Reset()
	again, err := watchStep(context.Background(), store, newID, 100, matchNetwork, &buf)
	if err != nil {
		t.Fatalf("watchStep second: %v", err)
	}
	if again != newID {
		t.Fatalf("second watchStep advanced id to %d, want %d", again, newID)
	}
	if buf.Len() != 0 {
		t.Fatalf("second watchStep produced output: %q", buf.String())
	}
}

func TestDaemonArgsForwardCustomIgnorePaths(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	cfg.DBPath = "/tmp/ltm.db"
	cfg.PIDFile = "/tmp/ltm.pid"
	cfg.IgnorePaths = append(cfg.IgnorePaths, "/tmp/custom", "/var/tmp/noisy")

	args := daemonArgs(cfg)
	want := []string{
		"--db", "/tmp/ltm.db",
		"--pidfile", "/tmp/ltm.pid",
		"--ignore-path", "/tmp/custom",
		"--ignore-path", "/var/tmp/noisy",
		"daemon", "--foreground",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("daemonArgs = %#v, want %#v", args, want)
	}
}

func TestPrintVersion(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := printVersion(&buf, false); err != nil {
		t.Fatalf("printVersion text: %v", err)
	}
	if !strings.Contains(buf.String(), Version) {
		t.Fatalf("text version output missing version %q:\n%s", Version, buf.String())
	}

	buf.Reset()
	if err := printVersion(&buf, true); err != nil {
		t.Fatalf("printVersion json: %v", err)
	}
	var info map[string]string
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		t.Fatalf("version json invalid: %v", err)
	}
	for _, key := range []string{"version", "commit", "date", "go", "os", "arch"} {
		if _, ok := info[key]; !ok {
			t.Fatalf("version json missing key %q: %v", key, info)
		}
	}
}

func TestFormatEvent(t *testing.T) {
	t.Parallel()
	line := formatEvent(storage.Event{
		Timestamp: time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC),
		Category:  "file",
		Action:    "write",
		PID:       100,
		Comm:      "nginx",
		Path:      "/etc/nginx/nginx.conf",
	})
	for _, want := range []string{"file", "write", "pid=100", "comm=nginx", "path=/etc/nginx/nginx.conf"} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatEvent output %q missing %q", line, want)
		}
	}
}
