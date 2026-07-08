package storage

import (
	"encoding/json"
	"time"
)

const SchemaVersion = 1

type Event struct {
	ID             int64           `json:"id,omitempty"`
	SchemaVersion  int             `json:"schema_version"`
	Timestamp      time.Time       `json:"timestamp"`
	Category       string          `json:"category"`
	Action         string          `json:"action"`
	PID            int             `json:"pid"`
	PPID           int             `json:"ppid"`
	UID            int             `json:"uid"`
	Comm           string          `json:"comm"`
	Exe            string          `json:"exe,omitempty"`
	ContainerID    string          `json:"container_id,omitempty"`
	CgroupPath     string          `json:"cgroup_path,omitempty"`
	Path           string          `json:"path,omitempty"`
	OldPath        string          `json:"old_path,omitempty"`
	LocalAddr      string          `json:"local_addr,omitempty"`
	LocalPort      int             `json:"local_port,omitempty"`
	RemoteAddr     string          `json:"remote_addr,omitempty"`
	RemotePort     int             `json:"remote_port,omitempty"`
	RemoteHost     string          `json:"remote_host,omitempty"`
	TargetPID      int             `json:"target_pid,omitempty"`
	ExitCode       int             `json:"exit_code,omitempty"`
	DroppedBefore   int64           `json:"dropped_before,omitempty"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
	Raw            json.RawMessage  `json:"raw,omitempty"`
}

type Process struct {
	PID         int       `json:"pid"`
	PPID        int       `json:"ppid"`
	UID         int       `json:"uid"`
	Comm        string    `json:"comm"`
	Exe         string    `json:"exe,omitempty"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time,omitempty"`
	ExitCode    int       `json:"exit_code,omitempty"`
	ContainerID string    `json:"container_id,omitempty"`
	CgroupPath  string    `json:"cgroup_path,omitempty"`
}

type FileRecord struct {
	Path       string    `json:"path"`
	LastAction string    `json:"last_action"`
	LastPID    int       `json:"last_pid"`
	LastComm   string    `json:"last_comm"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type SocketRecord struct {
	PID        int       `json:"pid"`
	Comm       string    `json:"comm"`
	LocalAddr  string    `json:"local_addr"`
	LocalPort  int       `json:"local_port"`
	RemoteAddr string    `json:"remote_addr,omitempty"`
	RemotePort int       `json:"remote_port,omitempty"`
	State      string    `json:"state"`
	SeenAt     time.Time `json:"seen_at"`
}

type Snapshot struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Label     string    `json:"label"`
	Summary   string    `json:"summary"`
}

type Status struct {
	EventCount    int64     `json:"event_count"`
	DroppedEvents int64     `json:"dropped_events"`
	LastEventTime time.Time `json:"last_event_time"`
	StartedAt     time.Time `json:"started_at"`
}

type InsertStats struct {
	Inserted     int
	Dropped      int64
	WriteLatency time.Duration
}

type DemoSpec struct {
	Start time.Time
	Count int
}

