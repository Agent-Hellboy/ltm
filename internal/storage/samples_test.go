package storage

import (
	"context"
	"testing"
	"time"
)

func TestSystemSampleRoundTripAndLatest(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	if _, ok, err := store.LatestSystemSample(ctx); err != nil || ok {
		t.Fatalf("empty store: got ok=%v err=%v, want ok=false", ok, err)
	}

	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	samples := []SystemSample{
		{Timestamp: base, CPUPct: 12.5, Load1: 1.2, MemTotalKB: 16000000, MemAvailableKB: 8000000, PSICPUSomeAvg10: 3.4},
		{Timestamp: base.Add(time.Second), CPUPct: 88.0, Load1: 4.5, MemAvailableKB: 500000, PSICPUSomeAvg10: 42.1, DiskWriteKB: 999},
	}
	if err := store.InsertSystemSamples(ctx, samples); err != nil {
		t.Fatalf("insert system samples: %v", err)
	}

	got, ok, err := store.LatestSystemSample(ctx)
	if err != nil || !ok {
		t.Fatalf("latest: ok=%v err=%v", ok, err)
	}
	if got.CPUPct != 88.0 || got.PSICPUSomeAvg10 != 42.1 || got.DiskWriteKB != 999 {
		t.Fatalf("latest sample = %+v, want the second (newest) row", got)
	}
	if !got.Timestamp.Equal(base.Add(time.Second)) {
		t.Errorf("latest timestamp = %s, want %s", got.Timestamp, base.Add(time.Second))
	}
}

func TestProcessSampleInsert(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	if err := store.InsertProcessSamples(ctx, []ProcessSample{
		{Timestamp: base, PID: 42, Comm: "postgres", State: "R", CPUPct: 180.5, RSSKB: 2100000, Threads: 12, Cgroup: "/system.slice/postgres"},
		{Timestamp: base, PID: 7, Comm: "sshd", State: "S", RSSKB: 4096},
	}); err != nil {
		t.Fatalf("insert process samples: %v", err)
	}

	var count int
	var topComm string
	var topCPU float64
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*), (SELECT comm FROM process_samples ORDER BY cpu_pct DESC LIMIT 1), (SELECT MAX(cpu_pct) FROM process_samples) FROM process_samples`,
	).Scan(&count, &topComm, &topCPU); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if count != 2 {
		t.Fatalf("got %d process rows, want 2", count)
	}
	if topComm != "postgres" || topCPU != 180.5 {
		t.Fatalf("busiest process = %s @ %.1f%%, want postgres @ 180.5", topComm, topCPU)
	}
}

func TestPruneCoversSampleTables(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)

	if _, err := store.InsertEvents(ctx, []Event{{Timestamp: old, Category: "file", Action: "read"}, {Timestamp: recent, Category: "file", Action: "read"}}); err != nil {
		t.Fatalf("events: %v", err)
	}
	if err := store.InsertSystemSamples(ctx, []SystemSample{{Timestamp: old}, {Timestamp: recent}}); err != nil {
		t.Fatalf("system: %v", err)
	}
	if err := store.InsertProcessSamples(ctx, []ProcessSample{{Timestamp: old, PID: 1}, {Timestamp: recent, PID: 2}}); err != nil {
		t.Fatalf("process: %v", err)
	}

	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	n, err := store.Prune(ctx, cutoff, false)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	// One old row from each of the three tables.
	if n != 3 {
		t.Fatalf("pruned %d rows, want 3 (one per time-series table)", n)
	}
}
