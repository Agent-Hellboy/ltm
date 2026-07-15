package daemon

import (
	"context"
	"time"

	"ltm/internal/collector"
	"ltm/internal/ebpf"
	"ltm/internal/storage"
)

type Config struct {
	IgnorePaths []string
	BatchSize   int
	FlushPeriod time.Duration
}

const (
	defaultBatchSize         = 4096
	defaultMaxFlushBatchSize = 16384
	defaultIngestBufferSize  = 65536
	defaultSourceBufferSize  = 65536
)

type Service struct {
	store *storage.Store
	cfg   Config
}

func NewService(store *storage.Store, cfg Config) *Service {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.FlushPeriod <= 0 {
		cfg.FlushPeriod = 1 * time.Second
	}
	return &Service{store: store, cfg: cfg}
}

func (s *Service) Run(ctx context.Context) error {
	return s.runWithSource(ctx, ebpf.NewSource())
}

// runWithSource builds a two-stage pipeline:
//
//	ebpf/src ──▶ collector (filter + source buffer) ──▶ ingest ──▶ flushLoop ──▶ store
//
// ingest is the pipeline entrance queue (buffered channel): it decouples capture
// from SQLite so a slow flush does not stall the collector.
//
// Shutdown order (producer quiesce, then close, then consumer drain):
//  1. cancel shared ctx
//  2. wait for collector (sole sender into ingest)
//  3. close(ingest) — no more sends are possible
//  4. wait for flushLoop to read until close and persist
func (s *Service) runWithSource(ctx context.Context, src ebpf.EventSource) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Entrance queue (Queuing): capacity tuned for bursty eBPF ingest.
	ingest := make(chan storage.Event, defaultIngestBufferSize)
	col := collector.New(collector.Config{
		IgnorePaths: s.cfg.IgnorePaths,
		BufferSize:  defaultSourceBufferSize,
	})

	colErr := make(chan error, 1)
	go func() {
		// Stage: generate/filter — ignore paths, bound the source buffer, count drops.
		colErr <- col.Run(ctx, src, ingest)
	}()
	flushErr := make(chan error, 1)
	go func() {
		// Stage: chunk/flush — size-or-time batches into InsertEvents.
		// Exits only after ingest is closed (see flushLoop).
		flushErr <- s.flushLoop(ctx, ingest, col.DroppedEvents)
	}()

	var runErr error
	colDone := false
	flushDone := false
	select {
	case <-ctx.Done():
	case runErr = <-colErr:
		colDone = true
	case runErr = <-flushErr:
		flushDone = true
	}

	cancel()
	if !colDone {
		if cerr := <-colErr; cerr != nil && runErr == nil {
			runErr = cerr
		}
	}
	// Producer has stopped; closing ingest is safe and unblocks the flusher.
	close(ingest)
	if !flushDone {
		if ferr := <-flushErr; ferr != nil && runErr == nil {
			runErr = ferr
		}
	}
	return runErr
}
