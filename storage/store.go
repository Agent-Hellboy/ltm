package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu sync.RWMutex

	path string
	file *os.File

	events    []Event
	processes map[int]Process
	files     map[string]FileRecord
	sockets   []SocketRecord
	startedAt time.Time
	dropped   int64
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("empty storage path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Store{
		path:      path,
		file:      f,
		processes: map[int]Process{},
		files:     map[string]FileRecord{},
	}
	if err := s.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(s.file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return err
		}
		if ev.SchemaVersion == 0 {
			ev.SchemaVersion = SchemaVersion
		}
		s.applyEventLocked(ev)
		s.events = append(s.events, ev)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := s.file.Seek(0, os.SEEK_END)
	return err
}

func (s *Store) InsertEvents(ctx context.Context, events []Event) (InsertStats, error) {
	if len(events) == 0 {
		return InsertStats{}, nil
	}
	start := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		return InsertStats{}, errors.New("store closed")
	}

	w := bufio.NewWriterSize(s.file, 64*1024)
	inserted := 0
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return InsertStats{Inserted: inserted, Dropped: s.dropped, WriteLatency: time.Since(start)}, err
		}
		if ev.SchemaVersion == 0 {
			ev.SchemaVersion = SchemaVersion
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().UTC()
		}
		if ev.Metadata == nil {
			ev.Metadata = map[string]any{}
		}
		if len(ev.Raw) == 0 {
			ev.Raw = json.RawMessage(`{}`)
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return InsertStats{Inserted: inserted, Dropped: s.dropped, WriteLatency: time.Since(start)}, err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return InsertStats{Inserted: inserted, Dropped: s.dropped, WriteLatency: time.Since(start)}, err
		}
		s.applyEventLocked(ev)
		s.events = append(s.events, ev)
		inserted++
	}
	if err := w.Flush(); err != nil {
		return InsertStats{Inserted: inserted, Dropped: s.dropped, WriteLatency: time.Since(start)}, err
	}
	if err := s.file.Sync(); err != nil {
		return InsertStats{Inserted: inserted, Dropped: s.dropped, WriteLatency: time.Since(start)}, err
	}
	return InsertStats{Inserted: inserted, Dropped: s.dropped, WriteLatency: time.Since(start)}, nil
}

func (s *Store) applyEventLocked(ev Event) {
	if ev.Timestamp.After(s.startedAt) {
		if s.startedAt.IsZero() {
			s.startedAt = ev.Timestamp
		} else {
			s.startedAt = minTime(s.startedAt, ev.Timestamp)
		}
	}
	s.dropped = ev.DroppedBefore
	switch ev.Category {
	case "process":
		switch ev.Action {
		case "exec", "fork", "clone":
			s.processes[ev.PID] = Process{
				PID:         ev.PID,
				PPID:        ev.PPID,
				UID:         ev.UID,
				Comm:        ev.Comm,
				Exe:         ev.Exe,
				StartTime:   ev.Timestamp,
				ContainerID: ev.ContainerID,
				CgroupPath:  ev.CgroupPath,
			}
		case "exit":
			p := s.processes[ev.PID]
			p.PID = ev.PID
			p.PPID = ev.PPID
			p.UID = ev.UID
			p.Comm = ev.Comm
			p.Exe = ev.Exe
			p.EndTime = ev.Timestamp
			p.ExitCode = ev.ExitCode
			s.processes[ev.PID] = p
		}
	case "file":
		if ev.Path != "" {
			s.files[ev.Path] = FileRecord{
				Path:       ev.Path,
				LastAction: ev.Action,
				LastPID:    ev.PID,
				LastComm:   ev.Comm,
				LastSeenAt: ev.Timestamp,
			}
		}
	case "network":
		if ev.LocalAddr != "" || ev.RemoteAddr != "" {
			s.sockets = append(s.sockets, SocketRecord{
				PID:        ev.PID,
				Comm:       ev.Comm,
				LocalAddr:  ev.LocalAddr,
				LocalPort:  ev.LocalPort,
				RemoteAddr: ev.RemoteAddr,
				RemotePort: ev.RemotePort,
				State:      ev.Action,
				SeenAt:     ev.Timestamp,
			})
		}
	}
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	last := time.Time{}
	if n := len(s.events); n > 0 {
		last = s.events[n-1].Timestamp
	}
	return Status{
		EventCount:    int64(len(s.events)),
		DroppedEvents: s.dropped,
		LastEventTime: last,
		StartedAt:     s.startedAt,
	}, nil
}

func (s *Store) EventsSince(ctx context.Context, since time.Time, limit int) ([]Event, error) {
	return s.EventsBetween(ctx, since, time.Now(), limit)
}

func (s *Store) EventsBetween(ctx context.Context, from, to time.Time, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]Event, 0, limit)
	for _, ev := range s.events {
		if ev.Timestamp.Before(from) || ev.Timestamp.After(to) {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Store) EventsByPID(ctx context.Context, pid int, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]Event, 0, limit)
	for _, ev := range s.events {
		if ev.PID != pid {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Store) EventsByPath(ctx context.Context, path string, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]Event, 0, limit)
	for _, ev := range s.events {
		if ev.Path != path && ev.OldPath != path {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Store) Processes(ctx context.Context, limit int) ([]Process, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	keys := make([]int, 0, len(s.processes))
	for pid := range s.processes {
		keys = append(keys, pid)
	}
	sort.Ints(keys)
	out := make([]Process, 0, min(limit, len(keys)))
	for i := len(keys) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.processes[keys[i]])
	}
	return out, nil
}

func (s *Store) Files(ctx context.Context, limit int) ([]FileRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	keys := make([]string, 0, len(s.files))
	for path := range s.files {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	out := make([]FileRecord, 0, min(limit, len(keys)))
	for i := len(keys) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.files[keys[i]])
	}
	return out, nil
}

func (s *Store) Sockets(ctx context.Context, limit int) ([]SocketRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	if len(s.sockets) <= limit {
		out := make([]SocketRecord, len(s.sockets))
		copy(out, s.sockets)
		return out, nil
	}
	out := make([]SocketRecord, 0, limit)
	for i := len(s.sockets) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.sockets[i])
	}
	return out, nil
}

func (s *Store) PathEvents(ctx context.Context, path string, limit int) ([]Event, error) {
	return s.EventsByPath(ctx, path, limit)
}

func (s *Store) QueryText(ctx context.Context, terms []string, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	terms = normalizeTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	out := make([]Event, 0, limit)
	for i := len(s.events) - 1; i >= 0; i-- {
		ev := s.events[i]
		if matchesTerms(ev, terms) {
			out = append(out, ev)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func matchesTerms(ev Event, terms []string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		ev.Category, ev.Action, ev.Comm, ev.Exe, ev.Path, ev.OldPath, ev.LocalAddr, ev.RemoteAddr, ev.RemoteHost, ev.ContainerID, ev.CgroupPath,
	}, " "))
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func normalizeTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" {
			out = append(out, term)
		}
	}
	return out
}

func GenerateDemoEvents(start time.Time, count int) []Event {
	if count <= 0 {
		count = 24
	}
	events := make([]Event, 0, count)
	base := start
	paths := []string{"/tmp/ltm-demo.txt", "/tmp/ltm-demo.yaml"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * 750 * time.Millisecond)
		switch i % 6 {
		case 0:
			events = append(events, Event{
				SchemaVersion: SchemaVersion,
				Timestamp:     ts,
				Category:      "process",
				Action:        "exec",
				PID:           4200 + i,
				PPID:          1,
				UID:           0,
				Comm:          fmt.Sprintf("demo-worker-%d", i),
				Exe:           "/usr/bin/demo-worker",
				Path:          "/usr/bin/demo-worker",
				Metadata:      map[string]any{"scenario": "demo"},
			})
		case 1:
			events = append(events, Event{
				SchemaVersion: SchemaVersion,
				Timestamp:     ts,
				Category:      "file",
				Action:        "write",
				PID:           4200 + i - 1,
				PPID:          1,
				UID:           0,
				Comm:          fmt.Sprintf("demo-worker-%d", i-1),
				Path:          paths[i%len(paths)],
				Metadata:      map[string]any{"bytes": 128},
			})
		case 2:
			events = append(events, Event{
				SchemaVersion: SchemaVersion,
				Timestamp:     ts,
				Category:      "network",
				Action:        "listen",
				PID:           4300,
				PPID:          1,
				UID:           0,
				Comm:          "demo-web",
				LocalAddr:     "127.0.0.1",
				LocalPort:     18080,
			})
		case 3:
			events = append(events, Event{
				SchemaVersion: SchemaVersion,
				Timestamp:     ts,
				Category:      "network",
				Action:        "connect",
				PID:           4310,
				PPID:          4300,
				UID:           1000,
				Comm:          "curl",
				RemoteAddr:    "127.0.0.1",
				RemotePort:    18080,
				RemoteHost:    "localhost",
			})
		case 4:
			events = append(events, Event{
				SchemaVersion: SchemaVersion,
				Timestamp:     ts,
				Category:      "file",
				Action:        "rename",
				PID:           4300,
				PPID:          1,
				UID:           0,
				Comm:          "demo-web",
				Path:          "/tmp/ltm-demo.txt",
				OldPath:       "/tmp/ltm-demo.txt.bak",
			})
		case 5:
			events = append(events, Event{
				SchemaVersion: SchemaVersion,
				Timestamp:     ts,
				Category:      "process",
				Action:        "exit",
				PID:           4300,
				PPID:          1,
				UID:           0,
				Comm:          "demo-web",
				Exe:           "/usr/bin/demo-web",
				ExitCode:      0,
			})
		}
	}
	return events
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
