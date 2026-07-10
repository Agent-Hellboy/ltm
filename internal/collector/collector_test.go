package collector

import (
	"context"
	"testing"
	"time"

	"ltm/internal/storage"
)

// staticSource emits a fixed set of events once, then blocks until the context
// is cancelled.
type staticSource struct {
	events []storage.Event
}

func (s staticSource) Run(ctx context.Context, out chan<- storage.Event) error {
	for _, ev := range s.events {
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

func TestCollectorFiltersIgnoredPaths(t *testing.T) {
	c := New(Config{IgnorePaths: []string{"/tmp/ignored"}})
	src := staticSource{events: []storage.Event{
		{Category: "file", Action: "write", Path: "/etc/keep.conf"},
		{Category: "file", Action: "write", Path: "/proc/1/status"},      // default ignore
		{Category: "file", Action: "write", Path: "/tmp/ignored/secret"}, // custom ignore
		{Category: "file", Action: "rename", OldPath: "/sys/kernel/x"},   // default ignore via old_path
		{Category: "network", Action: "connect", RemoteAddr: "10.0.0.1"}, // no path, kept
	}}

	out := make(chan storage.Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, src, out) }()

	var got []storage.Event
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case ev := <-out:
			got = append(got, ev)
		case <-deadline:
			cancel()
			t.Fatalf("timed out; got %d events, want 2", len(got))
		}
	}
	cancel()
	<-done

	// Only /etc/keep.conf and the address-less network event should survive.
	for _, ev := range got {
		if ev.Path == "/proc/1/status" || ev.Path == "/tmp/ignored/secret" || ev.OldPath == "/sys/kernel/x" {
			t.Fatalf("ignored path leaked through: %+v", ev)
		}
	}
	if len(got) != 2 {
		t.Fatalf("kept %d events, want 2: %+v", len(got), got)
	}
}

func TestShouldIgnore(t *testing.T) {
	c := New(Config{IgnorePaths: []string{"/home/u/.cache/", "  /tmp/noisy  "}})
	cases := map[string]bool{
		"":                      false,
		"/etc/nginx/nginx.conf": false,
		"/procession/status":    false,
		"/proc/1/status":        true,
		"/sys/kernel/debug":     true,
		"/dev/null":             true,
		"/home/u/.cache/x":      true,
		"/home/u/.cache-bak/x":  false,
		"/tmp/noisy/event":      true,
	}
	for path, want := range cases {
		if got := c.shouldIgnore(path); got != want {
			t.Errorf("shouldIgnore(%q) = %v, want %v", path, got, want)
		}
	}
}
