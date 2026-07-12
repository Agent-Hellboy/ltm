package diff

import (
	"context"
	"strconv"
	"strings"
	"time"

	"ltm/internal/storage"
)

type Engine struct {
	store *storage.Store
}

func NewEngine(store *storage.Store) *Engine {
	return &Engine{store: store}
}

type DiffReport struct {
	From                time.Time        `json:"from"`
	To                  time.Time        `json:"to"`
	NewProcesses        []ProcessChange  `json:"new_processes"`
	ExitedProcesses     []ProcessChange  `json:"exited_processes"`
	ModifiedFiles       []FileChange     `json:"modified_files"`
	DeletedFiles        []FileChange     `json:"deleted_files"`
	NewListeners        []SocketChange   `json:"new_listeners"`
	OutboundConnections []SocketChange   `json:"outbound_connections"`
	HotWriters          []HotWriter      `json:"hot_writers"`
	Restarts            []ProcessRestart `json:"restarts"`
}

type ProcessChange struct {
	Timestamp time.Time `json:"timestamp"`
	PID       int       `json:"pid"`
	Comm      string    `json:"comm"`
	Path      string    `json:"path,omitempty"`
	Action    string    `json:"action"`
}

type FileChange struct {
	Timestamp time.Time `json:"timestamp"`
	Path      string    `json:"path"`
	Count     int       `json:"count"`
	Action    string    `json:"action"`
}

type SocketChange struct {
	Timestamp time.Time `json:"timestamp"`
	PID       int       `json:"pid"`
	Comm      string    `json:"comm"`
	Socket    string    `json:"socket"`
	Action    string    `json:"action"`
}

type HotWriter struct {
	Path      string    `json:"path"`
	PID       int       `json:"pid"`
	Comm      string    `json:"comm"`
	Count     int       `json:"count"`
	Timestamp time.Time `json:"timestamp"`
}

type ProcessRestart struct {
	Comm      string    `json:"comm"`
	PID       int       `json:"pid"`
	Timestamp time.Time `json:"timestamp"`
}

func (e *Engine) Diff(ctx context.Context, from, to time.Time) (DiffReport, error) {
	events, err := e.store.Query(ctx, storage.Filter{From: from, To: to, OrderAsc: true, Limit: 5000})
	if err != nil {
		return DiffReport{}, err
	}
	report := DiffReport{From: from, To: to}
	writes := make(map[string][]storage.Event)
	lastExit := make(map[string]storage.Event)
	// A single process creation fires several events (sched fork, clone
	// syscall, then exec), so dedupe NewProcesses by pid to avoid inflating
	// the count.
	seenNewPID := make(map[int]bool)
	for _, ev := range events {
		switch ev.Category + ":" + ev.Action {
		case "process:exec", "process:fork", "process:clone":
			if !seenNewPID[ev.PID] {
				seenNewPID[ev.PID] = true
				report.NewProcesses = append(report.NewProcesses, ProcessChange{
					Timestamp: ev.Timestamp,
					PID:       ev.PID,
					Comm:      ev.Comm,
					Path:      ev.Exe,
					Action:    ev.Action,
				})
			}
			if priorExit, ok := lastExit[restartKey(ev)]; ok && ev.Timestamp.After(priorExit.Timestamp) {
				report.Restarts = append(report.Restarts, ProcessRestart{
					Comm:      ev.Comm,
					PID:       ev.PID,
					Timestamp: ev.Timestamp,
				})
			}
		case "process:exit":
			report.ExitedProcesses = append(report.ExitedProcesses, ProcessChange{
				Timestamp: ev.Timestamp,
				PID:       ev.PID,
				Comm:      ev.Comm,
				Path:      ev.Exe,
				Action:    ev.Action,
			})
			if key := restartKey(ev); key != "" {
				lastExit[key] = ev
			}
		case "file:write", "file:rename", "file:truncate", "file:chmod", "file:chown", "file:mkdir":
			// Reads are not modifications and are deliberately excluded.
			report.ModifiedFiles = append(report.ModifiedFiles, FileChange{
				Timestamp: ev.Timestamp,
				Path:      firstNonEmpty(ev.Path, ev.OldPath),
				Count:     1,
				Action:    ev.Action,
			})
			// Only content writes feed hot-writer detection.
			if ev.Action == "write" {
				writes[firstNonEmpty(ev.Path, ev.OldPath)] = append(writes[firstNonEmpty(ev.Path, ev.OldPath)], ev)
			}
		case "file:unlink", "file:rmdir":
			report.DeletedFiles = append(report.DeletedFiles, FileChange{
				Timestamp: ev.Timestamp,
				Path:      ev.Path,
				Count:     1,
				Action:    ev.Action,
			})
		case "network:listen", "network:bind":
			report.NewListeners = append(report.NewListeners, SocketChange{
				Timestamp: ev.Timestamp,
				PID:       ev.PID,
				Comm:      ev.Comm,
				Socket:    ev.LocalAddr + ":" + strconv.Itoa(ev.LocalPort),
				Action:    ev.Action,
			})
		case "network:connect", "network:send":
			report.OutboundConnections = append(report.OutboundConnections, SocketChange{
				Timestamp: ev.Timestamp,
				PID:       ev.PID,
				Comm:      ev.Comm,
				Socket:    ev.RemoteAddr + ":" + strconv.Itoa(ev.RemotePort),
				Action:    ev.Action,
			})
		}
	}
	for path, evs := range writes {
		if len(evs) >= 3 {
			report.HotWriters = append(report.HotWriters, HotWriter{
				Path:      path,
				PID:       evs[len(evs)-1].PID,
				Comm:      evs[len(evs)-1].Comm,
				Count:     len(evs),
				Timestamp: evs[len(evs)-1].Timestamp,
			})
		}
	}
	return report, nil
}

func restartKey(ev storage.Event) string {
	if strings.TrimSpace(ev.Comm) != "" {
		return ev.Comm
	}
	if ev.PID != 0 {
		return strconv.Itoa(ev.PID)
	}
	return ""
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}
