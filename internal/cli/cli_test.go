package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	store := newTestStore(t)

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

func TestParseGlobalFlagsAfterSubcommand(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()

	rest, err := parseGlobalFlags([]string{"--db", "/tmp/one.db", "--pidfile", "/tmp/one.pid", "--ignore-path", "/tmp/extra", "query", "hello"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalFlags: %v", err)
	}

	if cfg.DBPath != "/tmp/one.db" {
		t.Fatalf("DBPath = %q, want /tmp/one.db", cfg.DBPath)
	}
	if cfg.PIDFile != "/tmp/one.pid" {
		t.Fatalf("PIDFile = %q, want /tmp/one.pid", cfg.PIDFile)
	}
	if !reflect.DeepEqual(rest, []string{"query", "hello"}) {
		t.Fatalf("rest = %#v, want %#v", rest, []string{"query", "hello"})
	}
	if got := cfg.IgnorePaths[len(cfg.IgnorePaths)-1]; got != "/tmp/extra" {
		t.Fatalf("last ignore path = %q, want /tmp/extra", got)
	}
}

func TestParseGlobalFlagsStopsAtFirstUnrecognizedFlag(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()

	// A command-specific flag (e.g. daemon's --foreground) must not be
	// scanned through to find a global flag placed after it — global flags
	// only apply from the head of the args, matching stdlib flag.Parse
	// semantics that every subcommand's own flag.FlagSet already relies on.
	rest, err := parseGlobalFlags([]string{"--db", "/tmp/one.db", "--foreground", "--json"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalFlags: %v", err)
	}

	if cfg.DBPath != "/tmp/one.db" {
		t.Fatalf("DBPath = %q, want /tmp/one.db", cfg.DBPath)
	}
	if cfg.JSON {
		t.Fatal("JSON flag should not be applied once scanning stops at --foreground")
	}
	if !reflect.DeepEqual(rest, []string{"--foreground", "--json"}) {
		t.Fatalf("rest = %#v, want %#v", rest, []string{"--foreground", "--json"})
	}
}

func TestParseGlobalFlagsDoesNotConsumeFreeTextArguments(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	cfg.DBPath = "/tmp/default.db"

	// A `query` question containing flag-shaped words must pass through
	// untouched instead of being mistaken for global flags anywhere in it.
	rest, err := parseGlobalFlags([]string{"what", "changed", "under", "--db", "recently"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalFlags: %v", err)
	}
	if cfg.DBPath != "/tmp/default.db" {
		t.Fatalf("DBPath = %q, want unchanged default", cfg.DBPath)
	}
	want := []string{"what", "changed", "under", "--db", "recently"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
}

func TestParseGlobalFlagsSingleDash(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()

	rest, err := parseGlobalFlags([]string{"-db", "/tmp/one.db", "-json", "status"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalFlags: %v", err)
	}
	if cfg.DBPath != "/tmp/one.db" {
		t.Fatalf("DBPath = %q, want /tmp/one.db", cfg.DBPath)
	}
	if !cfg.JSON {
		t.Fatal("JSON flag was not applied via single-dash -json")
	}
	if !reflect.DeepEqual(rest, []string{"status"}) {
		t.Fatalf("rest = %#v, want %#v", rest, []string{"status"})
	}
}

func TestParseGlobalFlagsJSONEqualsBoolValue(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	cfg.JSON = true

	rest, err := parseGlobalFlags([]string{"--json=false", "prune"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalFlags: %v", err)
	}
	if cfg.JSON {
		t.Fatal("--json=false should clear JSON")
	}
	if !reflect.DeepEqual(rest, []string{"prune"}) {
		t.Fatalf("rest = %#v, want %#v", rest, []string{"prune"})
	}
}

func TestRunWatchRejectsNonPositiveInterval(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "missing.db")

	for _, interval := range []string{"0", "-1s"} {
		err := runWatch(cfg, []string{"--interval", interval})
		if err == nil {
			t.Fatalf("runWatch(--interval %s) = nil error, want a usage error", interval)
		}
		if !strings.Contains(err.Error(), "interval must be positive") {
			t.Fatalf("runWatch(--interval %s) error = %q, want it to mention a positive interval", interval, err)
		}
	}
}

func TestSignalContextCancelIsIdempotent(t *testing.T) {
	oldNotify, oldStop := signalNotify, signalStop
	t.Cleanup(func() {
		signalNotify = oldNotify
		signalStop = oldStop
	})

	notifyCh := make(chan chan<- os.Signal, 1)
	stopCh := make(chan struct{}, 2)
	signalNotify = func(ch chan<- os.Signal) {
		notifyCh <- ch
	}
	signalStop = func(ch chan<- os.Signal) {
		stopCh <- struct{}{}
	}

	ctx, cancel := signalContext()
	select {
	case <-notifyCh:
	case <-time.After(time.Second):
		t.Fatal("signalNotify was not called")
	}

	cancel()
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not cancelled")
	}
	select {
	case <-stopCh:
	case <-time.After(time.Second):
		t.Fatal("signalStop was not called")
	}
	select {
	case <-stopCh:
		t.Fatal("signalStop called more than once")
	default:
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
