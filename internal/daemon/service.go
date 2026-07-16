package daemon

import (
	"context"
	"path/filepath"
	"time"

	"ltm/internal/collector"
	"ltm/internal/ebpf"
	"ltm/internal/storage"
)

type Config struct {
	IgnorePaths []string
	// DBPath and PIDFile are the recorder's own runtime files. The daemon adds
	// them (and the SQLite WAL/SHM/journal sidecars) to the collector's ignore
	// rules automatically, so activity ltm generates against its own store is
	// never captured — the user never has to pass --ignore-path for it. This is
	// the userspace half of the feedback-loop guard; the kernel half filters by
	// the "ltm" program name (see should_skip in collector.bpf.c).
	DBPath      string
	PIDFile     string
	BatchSize   int
	FlushPeriod time.Duration
	// SystemSampleEvery / ProcessSampleEvery set the resource-sampling cadence
	// (Phase 1: /proc + PSI). Zero uses the defaults; a negative value disables
	// that sampler.
	SystemSampleEvery  time.Duration
	ProcessSampleEvery time.Duration
}

const (
	defaultBatchSize         = 4096
	defaultMaxFlushBatchSize = 16384
	defaultIngestBufferSize  = 65536
	defaultSourceBufferSize  = 65536

	defaultSystemSampleEvery  = 1 * time.Second
	defaultProcessSampleEvery = 5 * time.Second
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
	if cfg.SystemSampleEvery == 0 {
		cfg.SystemSampleEvery = defaultSystemSampleEvery
	}
	if cfg.ProcessSampleEvery == 0 {
		cfg.ProcessSampleEvery = defaultProcessSampleEvery
	}
	return &Service{store: store, cfg: cfg}
}

func (s *Service) Run(ctx context.Context) error {
	return s.runWithSource(ctx, ebpf.NewSource())
}

// withSelfIgnores appends the recorder's own runtime files to the caller's
// ignore paths so ltm never captures its own reads/writes of the SQLite store
// or pid file. WAL mode keeps -wal/-shm sidecars (and -journal in rollback
// mode) with their own I/O, so all are covered. Paths are resolved to absolute
// form because the collector filter matches against the absolute paths eBPF
// observes, and storage.Open opens the DB by its absolute path.
func withSelfIgnores(ignore []string, dbPath, pidFile string) []string {
	out := append([]string{}, ignore...)
	add := func(p string) {
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		out = append(out, p)
	}
	if dbPath != "" {
		if abs, err := filepath.Abs(dbPath); err == nil {
			dbPath = abs
		}
		out = append(out, dbPath, dbPath+"-wal", dbPath+"-shm", dbPath+"-journal")
	}
	add(pidFile)
	return out
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
		IgnorePaths: withSelfIgnores(s.cfg.IgnorePaths, s.cfg.DBPath, s.cfg.PIDFile),
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

	// Independent side timeline: /proc + PSI resource samples written straight
	// to the store on their own cadence. It does not feed the event pipeline,
	// so it is joined separately (before the store is closed by the caller).
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		s.sampleLoop(ctx)
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
	<-sampleDone // sampler stops on cancel; join before the store closes
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
