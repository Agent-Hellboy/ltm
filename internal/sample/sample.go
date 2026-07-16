package sample

import (
	"errors"

	"ltm/internal/storage"
)

// ErrUnsupported is returned by the sampler on non-Linux platforms.
var ErrUnsupported = errors.New("sampling is only supported on Linux")

// Sampler produces machine-state samples on demand. System() is meant to be
// called on the fast cadence (~1s) and Processes() on the slow one (~5s). A
// Sampler is NOT safe for concurrent use: rate fields are computed from state
// held since the previous call, so a single caller must serialize calls.
type Sampler interface {
	// System returns one system-wide sample. Rate fields (cpu_pct, disk_*,
	// net_*) cover the interval since the previous System call; the first call
	// after construction reports zero rates.
	System() (storage.SystemSample, error)
	// Processes returns one sample per live process. cpu_pct covers the
	// interval since the previous Processes call.
	Processes() ([]storage.ProcessSample, error)
}
