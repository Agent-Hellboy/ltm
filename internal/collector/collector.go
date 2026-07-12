package collector

import (
	"context"
	"errors"
	"sync/atomic"

	"ltm/internal/ebpf"
	"ltm/internal/storage"
)

type Config struct {
	IgnorePaths []string
	BufferSize  int
}

type Collector struct {
	cfg         Config
	dropped     int64
	ignoreRules []string
}

func New(cfg Config) *Collector {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1024
	}
	return &Collector{
		cfg:         cfg,
		ignoreRules: newIgnoreRules(cfg.IgnorePaths),
	}
}

func (c *Collector) DroppedEvents() int64 {
	return atomic.LoadInt64(&c.dropped)
}

func (c *Collector) Run(ctx context.Context, src ebpf.EventSource, out chan<- storage.Event) error {
	in := make(chan storage.Event, c.cfg.BufferSize)
	errCh := make(chan error, 1)
	go func() {
		err := src.Run(ctx, in)
		close(in)
		if err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
		close(errCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				return err
			}
		case ev, ok := <-in:
			if !ok {
				return nil
			}
			if c.shouldIgnore(ev.Path) || c.shouldIgnore(ev.OldPath) {
				continue
			}
			select {
			case out <- ev:
			default:
				atomic.AddInt64(&c.dropped, 1)
			}
		}
	}
}

func (c *Collector) shouldIgnore(path string) bool {
	return shouldIgnorePath(c.ignoreRules, path)
}
