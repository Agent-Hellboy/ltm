package storage

import (
	"encoding/json"
	"errors"
	"time"
)

type Event struct {
	ID            int64           `json:"id,omitempty"`
	SchemaVersion int             `json:"schema_version"`
	Timestamp     time.Time       `json:"timestamp"`
	Category      string          `json:"category"`
	Action        string          `json:"action"`
	PID           int             `json:"pid"`
	PPID          int             `json:"ppid"`
	UID           int             `json:"uid"`
	Comm          string          `json:"comm"`
	Exe           string          `json:"exe,omitempty"`
	ContainerID   string          `json:"container_id,omitempty"`
	CgroupPath    string          `json:"cgroup_path,omitempty"`
	Path          string          `json:"path,omitempty"`
	OldPath       string          `json:"old_path,omitempty"`
	LocalAddr     string          `json:"local_addr,omitempty"`
	LocalPort     int             `json:"local_port,omitempty"`
	RemoteAddr    string          `json:"remote_addr,omitempty"`
	RemotePort    int             `json:"remote_port,omitempty"`
	RemoteHost    string          `json:"remote_host,omitempty"`
	TargetPID     int             `json:"target_pid,omitempty"`
	ExitCode      int             `json:"exit_code,omitempty"`
	DroppedBefore int64           `json:"dropped_before,omitempty"`
	Metadata      map[string]any  `json:"metadata,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

type SocketRecord struct {
	PID       int       `json:"pid"`
	Comm      string    `json:"comm"`
	LocalPort int       `json:"local_port"`
	State     string    `json:"state"`
	SeenAt    time.Time `json:"seen_at"`
}

type Status struct {
	EventCount    int64     `json:"event_count"`
	DroppedEvents int64     `json:"dropped_events"`
	LastEventTime time.Time `json:"last_event_time"`
	StartedAt     time.Time `json:"started_at"`
}

// SourceCount is a per-process share of recent event volume, used to point at
// whatever is flooding the recorder when drops appear (e.g. a feedback loop).
type SourceCount struct {
	Comm  string `json:"comm"`
	Exe   string `json:"exe,omitempty"`
	Count int64  `json:"count"`
}

type InsertStats struct {
	Dropped      int64
	WriteLatency time.Duration
}

// SystemSample is one system-wide resource sample (~1s cadence). Rates
// (cpu_pct, disk_*, net_*) are computed over the interval since the previous
// sample by the sampler; the rest are point-in-time gauges.
type SystemSample struct {
	ID              int64     `json:"id,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	CPUPct          float64   `json:"cpu_pct"`
	Load1           float64   `json:"load1"`
	Load5           float64   `json:"load5"`
	Load15          float64   `json:"load15"`
	ProcsRunning    int       `json:"procs_running"`
	ProcsBlocked    int       `json:"procs_blocked"`
	MemTotalKB      int64     `json:"mem_total_kb"`
	MemAvailableKB  int64     `json:"mem_available_kb"`
	SwapTotalKB     int64     `json:"swap_total_kb"`
	SwapFreeKB      int64     `json:"swap_free_kb"`
	PSICPUSomeAvg10 float64   `json:"psi_cpu_some_avg10"`
	PSIMemSomeAvg10 float64   `json:"psi_mem_some_avg10"`
	PSIMemFullAvg10 float64   `json:"psi_mem_full_avg10"`
	PSIIOSomeAvg10  float64   `json:"psi_io_some_avg10"`
	PSIIOFullAvg10  float64   `json:"psi_io_full_avg10"`
	DiskReadKB      int64     `json:"disk_read_kb"`
	DiskWriteKB     int64     `json:"disk_write_kb"`
	NetRxKB         int64     `json:"net_rx_kb"`
	NetTxKB         int64     `json:"net_tx_kb"`
	NetRxErrs       int64     `json:"net_rx_errs"`
	NetTxErrs       int64     `json:"net_tx_errs"`
	NetRxDrop       int64     `json:"net_rx_drop"`
	NetTxDrop       int64     `json:"net_tx_drop"`
}

// ProcessSample is one process's resource sample at a tick (~5s cadence).
// read_bytes/write_bytes are cumulative process totals (rchar/wchar); take the
// difference between two samples for a rate.
type ProcessSample struct {
	ID         int64     `json:"id,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
	PID        int       `json:"pid"`
	Comm       string    `json:"comm"`
	State      string    `json:"state"`
	CPUPct     float64   `json:"cpu_pct"`
	RSSKB      int64     `json:"rss_kb"`
	Threads    int       `json:"threads"`
	ReadBytes  int64     `json:"read_bytes"`
	WriteBytes int64     `json:"write_bytes"`
	Cgroup     string    `json:"cgroup"`
}

// Filter expresses a logical-AND set of predicates over the event log so
// callers can query by any combination of time, pid, path, process, etc.
// without hand-writing SQL.
type Filter struct {
	From, To   time.Time
	PIDs       []int
	UIDs       []int
	Categories []string
	Actions    []string
	Comms      []string
	// PathLike matches events whose path or old_path matches this SQL LIKE
	// pattern (% wildcard), so a rename is found by either its old or new name.
	PathLike string
	ExeLike  string
	// ExactPath matches events whose path or old_path equals this value
	// exactly (e.g. "who touched this file"), distinct from the PathLike
	// wildcard filter above.
	ExactPath string
	// OrderAsc returns oldest-first instead of Query's default newest-first,
	// for timelines and diffs that read chronologically.
	OrderAsc bool
	Limit    int
}

// Validate rejects a Filter that can't produce a sensible result: an inverted
// time range (From after To) always matches nothing, and a negative Limit
// isn't a valid SQL LIMIT.
func (f Filter) Validate() error {
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return errors.New("filter: From must not be after To")
	}
	if f.Limit < 0 {
		return errors.New("filter: Limit must not be negative")
	}
	return nil
}
