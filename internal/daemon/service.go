package daemon

import (
	"context"
	"fmt"
	"time"

	"ltm/internal/collector"
	"ltm/internal/ebpf"
	"ltm/internal/storage"
)

type Config struct {
	Mode        string
	IgnorePaths  []string
	BufferSize   int
	BatchSize    int
	FlushPeriod  time.Duration
}

type Service struct {
	store *storage.Store
	cfg   Config
}

func NewService(store *storage.Store, cfg Config) *Service {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 2048
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 128
	}
	if cfg.FlushPeriod <= 0 {
		cfg.FlushPeriod = 1 * time.Second
	}
	if cfg.Mode == "" {
		cfg.Mode = "demo"
	}
	return &Service{store: store, cfg: cfg}
}

func (s *Service) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ingest := make(chan storage.Event, s.cfg.BufferSize)
	col := collector.New(collector.Config{
		IgnorePaths: s.cfg.IgnorePaths,
		BufferSize:  s.cfg.BufferSize / 2,
	})

	sources := []ebpf.Source{demoSource{interval: 500 * time.Millisecond}}
	if s.cfg.Mode == "ebpf" {
		sources = []ebpf.Source{ebpf.RealCollector{}}
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- col.Run(ctx, sources, ingest)
	}()
	go func() {
		errCh <- s.flushLoop(ctx, ingest)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	}
}

func (s *Service) flushLoop(ctx context.Context, ingest <-chan storage.Event) error {
	ticker := time.NewTicker(s.cfg.FlushPeriod)
	defer ticker.Stop()
	batch := make([]storage.Event, 0, s.cfg.BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := s.store.InsertEvents(ctx, batch)
		batch = batch[:0]
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return flush()
		case ev, ok := <-ingest:
			if !ok {
				return flush()
			}
			ev.SchemaVersion = storage.SchemaVersion
			batch = append(batch, ev)
			if len(batch) >= s.cfg.BatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

type demoSource struct {
	interval time.Duration
}

func (d demoSource) Name() string { return "demo" }

func (d demoSource) Run(ctx context.Context, out chan<- storage.Event) error {
	if d.interval <= 0 {
		d.interval = 500 * time.Millisecond
	}
	events := storage.GenerateDemoEvents(time.Now(), 60)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			ev := events[i%len(events)]
			ev.Timestamp = time.Now()
			select {
			case out <- ev:
			default:
			}
			i++
		}
	}
}

func (s *Service) String() string {
	return fmt.Sprintf("mode=%s", s.cfg.Mode)
}
