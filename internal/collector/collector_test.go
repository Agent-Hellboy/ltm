package collector

import (
	"context"
	"errors"
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

// errorSource emits events then returns a real capture error (not cancel).
type errorSource struct {
	events []storage.Event
	err    error
}

func (s errorSource) Run(ctx context.Context, out chan<- storage.Event) error {
	for _, ev := range s.events {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

func TestCollectorReturnsSourceError(t *testing.T) {
	// Closed-in vs source-result used to race in select; exercise repeatedly.
	srcErr := errors.New("capture boom")
	for attempt := 0; attempt < 50; attempt++ {
		c := New(Config{BufferSize: 8})
		src := errorSource{
			events: []storage.Event{{Category: "process", Action: "exec", PID: 1}},
			err:    srcErr,
		}
		out := make(chan storage.Event, 8)
		err := c.Run(context.Background(), src, out)
		if !errors.Is(err, srcErr) {
			t.Fatalf("attempt %d: Run error = %v, want %v", attempt, err, srcErr)
		}
		select {
		case <-out:
		default:
			t.Fatalf("attempt %d: expected the pre-error event to be forwarded", attempt)
		}
	}
}

func TestCollectorDrainsSourceEventsOnCancel(t *testing.T) {
	c := New(Config{BufferSize: 64})
	events := make([]storage.Event, 32)
	for i := range events {
		events[i] = storage.Event{Category: "file", Action: "write", PID: i + 1, Path: "/tmp/x"}
	}
	src := staticSource{events: events}
	out := make(chan storage.Event, 64)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx, src, out) }()

	// Let the source fill the collector input buffer, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run: %v", err)
	}

	var n int
	for {
		select {
		case <-out:
			n++
		default:
			if n == 0 {
				t.Fatal("expected drained source events to be forwarded on cancel")
			}
			return
		}
	}
}
