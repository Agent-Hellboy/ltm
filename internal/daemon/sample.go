package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"ltm/internal/sample"
	"ltm/internal/storage"
)

// sampleLoop writes the resource-sampling timeline: a system-wide sample on
// the fast cadence and a per-process sweep on the slow one, each straight to
// the store. It returns when ctx is cancelled (no final partial write). On
// platforms without sampling support, or when both cadences are disabled, it
// is a no-op that just waits for cancellation.
func (s *Service) sampleLoop(ctx context.Context) {
	if !sample.Supported() {
		return
	}
	sysC, stopSys := ticker(s.cfg.SystemSampleEvery)
	defer stopSys()
	procC, stopProc := ticker(s.cfg.ProcessSampleEvery)
	defer stopProc()
	if sysC == nil && procC == nil {
		<-ctx.Done()
		return
	}

	sampler := sample.New()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sysC:
			sm, err := sampler.System()
			if err != nil {
				s.sampleWarn("system", err)
				continue
			}
			if err := s.writeSystemSample(sm); err != nil {
				s.sampleWarn("write system sample", err)
			}
		case <-procC:
			procs, err := sampler.Processes()
			if err != nil {
				s.sampleWarn("processes", err)
				continue
			}
			if err := s.writeProcessSamples(procs); err != nil {
				s.sampleWarn("write process samples", err)
			}
		}
	}
}

func (s *Service) writeSystemSample(sm storage.SystemSample) error {
	ctx, cancel := context.WithTimeout(context.Background(), flushWriteTimeout)
	defer cancel()
	return s.store.InsertSystemSamples(ctx, []storage.SystemSample{sm})
}

func (s *Service) writeProcessSamples(procs []storage.ProcessSample) error {
	if len(procs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushWriteTimeout)
	defer cancel()
	return s.store.InsertProcessSamples(ctx, procs)
}

// sampleWarn reports a sampling error to stderr without stopping the loop —
// one bad tick (e.g. a transient /proc read) must not kill the timeline.
func (s *Service) sampleWarn(what string, err error) {
	fmt.Fprintf(os.Stderr, "ltm: sample %s: %v\n", what, err)
}

// ticker returns a tick channel for d, or a nil channel (never fires) plus a
// no-op stop when d <= 0, so a disabled cadence doesn't panic time.NewTicker.
func ticker(d time.Duration) (<-chan time.Time, func()) {
	if d <= 0 {
		return nil, func() {}
	}
	t := time.NewTicker(d)
	return t.C, t.Stop
}
