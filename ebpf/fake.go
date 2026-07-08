package ebpf

import (
	"context"
	"math/rand"
	"time"

	"ltm/storage"
)

type FakeCollector struct {
	Scenario string
	Interval time.Duration
}

func (f FakeCollector) Name() string { return "fake" }

func (f FakeCollector) Run(ctx context.Context, out chan<- storage.Event) error {
	if f.Interval <= 0 {
		f.Interval = 500 * time.Millisecond
	}
	events := storage.GenerateDemoEvents(time.Now(), 30)
	ticker := time.NewTicker(f.Interval)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			ev := events[i%len(events)]
			ev.Timestamp = time.Now().Add(time.Duration(rand.Intn(25)) * time.Millisecond)
			ev.DroppedBefore = 0
			select {
			case out <- ev:
			default:
			}
			i++
		}
	}
}
