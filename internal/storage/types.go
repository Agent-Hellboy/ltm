package storage

import (
	"encoding/json"
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

type InsertStats struct {
	Dropped      int64
	WriteLatency time.Duration
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
	PathLike   string
	ExeLike    string
	Limit      int
}
