package collector

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"ltm/internal/ebpf"
	"ltm/internal/storage"
)

type Config struct {
	IgnorePaths []string
	BufferSize  int
}

type Stats struct {
	DroppedEvents int64
}

type Collector struct {
	cfg         Config
	stats       Stats
	ignoreRules []string
}

func New(cfg Config) *Collector {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1024
	}
	base := []string{"/proc", "/sys", "/dev", "/var/cache/apt", "/var/cache/dnf", "/var/cache/pacman"}
	return &Collector{cfg: cfg, ignoreRules: append(base, cfg.IgnorePaths...)}
}

func (c *Collector) Stats() Stats {
	return Stats{DroppedEvents: atomic.LoadInt64(&c.stats.DroppedEvents)}
}

func (c *Collector) Run(ctx context.Context, sources []ebpf.Source, out chan<- storage.Event) error {
	if len(sources) == 0 {
		return nil
	}
	in := make(chan storage.Event, c.cfg.BufferSize)
	errCh := make(chan error, len(sources))
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Go(func() {
			if err := src.Run(ctx, in); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case errCh <- err:
				default:
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(in)
		close(errCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errCh:
			if !ok {
				// Sources have finished; stop selecting on the closed channel
				// (a closed channel is always ready and would busy-spin).
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
				atomic.AddInt64(&c.stats.DroppedEvents, 1)
			}
		}
	}
}

func (c *Collector) shouldIgnore(path string) bool {
	if path == "" {
		return false
	}
	normalized := filepath.Clean(path)
	for _, rule := range c.ignoreRules {
		// Match the rule itself or a path beneath it, so "/proc" does not also
		// swallow "/procession".
		if normalized == rule || strings.HasPrefix(normalized, rule+"/") {
			return true
		}
	}
	return false
}
