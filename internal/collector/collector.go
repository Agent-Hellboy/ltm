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
	// done carries the source's terminal result after close(in). Closed in only
	// means "no more events"; the result is read from done so a source error
	// cannot be lost to a select race with the closed in case.
	done := make(chan error, 1)
	go func() {
		err := src.Run(ctx, in)
		close(in)
		done <- err
	}()

	forward := func(ev storage.Event) {
		if c.shouldIgnore(ev.Path) || c.shouldIgnore(ev.OldPath) {
			return
		}
		select {
		case out <- ev:
		default:
			atomic.AddInt64(&c.dropped, 1)
		}
	}

	finish := func() error {
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			// Drain already-produced events, then join the source worker.
			for ev := range in {
				forward(ev)
			}
			return finish()
		case ev, ok := <-in:
			if !ok {
				return finish()
			}
			forward(ev)
		}
	}
}

func (c *Collector) shouldIgnore(path string) bool {
	return shouldIgnorePath(c.ignoreRules, path)
}
