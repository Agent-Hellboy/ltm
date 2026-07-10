package collector

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	base := []string{"/proc", "/sys", "/dev", "/var/cache/apt", "/var/cache/dnf", "/var/cache/pacman"}
	return &Collector{cfg: cfg, ignoreRules: normalizeIgnoreRules(append(base, cfg.IgnorePaths...))}
}

func (c *Collector) DroppedEvents() int64 {
	return atomic.LoadInt64(&c.dropped)
}

func (c *Collector) Run(ctx context.Context, src ebpf.Source, out chan<- storage.Event) error {
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

func normalizeIgnoreRules(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
