package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS events (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		ts             INTEGER NOT NULL,
		category       TEXT NOT NULL DEFAULT '',
		action         TEXT NOT NULL DEFAULT '',
		pid            INTEGER NOT NULL DEFAULT 0,
		ppid           INTEGER NOT NULL DEFAULT 0,
		uid            INTEGER NOT NULL DEFAULT 0,
		comm           TEXT NOT NULL DEFAULT '',
		exe            TEXT NOT NULL DEFAULT '',
		container_id   TEXT NOT NULL DEFAULT '',
		cgroup_path    TEXT NOT NULL DEFAULT '',
		path           TEXT NOT NULL DEFAULT '',
		old_path       TEXT NOT NULL DEFAULT '',
		local_addr     TEXT NOT NULL DEFAULT '',
		local_port     INTEGER NOT NULL DEFAULT 0,
		remote_addr    TEXT NOT NULL DEFAULT '',
		remote_port    INTEGER NOT NULL DEFAULT 0,
		remote_host    TEXT NOT NULL DEFAULT '',
		target_pid     INTEGER NOT NULL DEFAULT 0,
		exit_code      INTEGER NOT NULL DEFAULT 0,
		dropped_before INTEGER NOT NULL DEFAULT 0,
		metadata       TEXT NOT NULL DEFAULT '{}',
		raw            TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts)`,
	`CREATE INDEX IF NOT EXISTS idx_events_pid_ts ON events(pid, ts)`,
	`CREATE INDEX IF NOT EXISTS idx_events_path ON events(path)`,
	`CREATE INDEX IF NOT EXISTS idx_events_cat_action_ts ON events(category, action, ts)`,
}

const eventColumns = `id, ts, category, action, pid, ppid, uid, comm, exe, container_id, cgroup_path, ` +
	`path, old_path, local_addr, local_port, remote_addr, remote_port, remote_host, target_pid, ` +
	`exit_code, dropped_before, metadata, raw`

const insertColumns = `ts, category, action, pid, ppid, uid, comm, exe, container_id, cgroup_path, ` +
	`path, old_path, local_addr, local_port, remote_addr, remote_port, remote_host, target_pid, ` +
	`exit_code, dropped_before, metadata, raw`

const insertPlaceholders = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`

type Store struct {
	db       *sql.DB
	readOnly bool
}

// Open opens (creating if necessary) a writable SQLite-backed store. Only the
// daemon should hold a writable Store at a time.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("empty storage path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	for _, stmt := range schemaStatements {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("init schema: %w", err)
		}
	}
	return s, nil
}

// OpenReadOnly opens an existing store for querying only. Every statement on
// the connection runs with PRAGMA query_only=ON, so writes fail instead of
// contending with the daemon's writer connection.
func OpenReadOnly(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("empty storage path")
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no ltm database at %s; run %q first", path, "ltm start")
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	return &Store{db: db, readOnly: true}, nil
}

func dsn(path string, readOnly bool) string {
	pragmas := "_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)&_pragma=synchronous(normal)"
	if readOnly {
		pragmas += "&_pragma=query_only(true)"
	}
	return "file:" + path + "?" + pragmas
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) InsertEvents(ctx context.Context, events []Event) (InsertStats, error) {
	if len(events) == 0 {
		return InsertStats{}, nil
	}
	start := time.Now()
	if s.db == nil {
		return InsertStats{}, errors.New("store closed")
	}
	if s.readOnly {
		return InsertStats{}, errors.New("store opened read-only")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InsertStats{}, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events (`+insertColumns+`) VALUES (`+insertPlaceholders+`)`)
	if err != nil {
		return InsertStats{}, err
	}
	defer stmt.Close()

	inserted := 0
	var dropped int64
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return InsertStats{Inserted: inserted, Dropped: dropped, WriteLatency: time.Since(start)}, err
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().UTC()
		}
		metadata, err := json.Marshal(ev.Metadata)
		if err != nil {
			return InsertStats{Inserted: inserted, Dropped: dropped, WriteLatency: time.Since(start)}, err
		}
		raw := ev.Raw
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if _, err := stmt.ExecContext(ctx,
			ev.Timestamp.UnixNano(), ev.Category, ev.Action, ev.PID, ev.PPID, ev.UID, ev.Comm, ev.Exe,
			ev.ContainerID, ev.CgroupPath, ev.Path, ev.OldPath, ev.LocalAddr, ev.LocalPort,
			ev.RemoteAddr, ev.RemotePort, ev.RemoteHost, ev.TargetPID, ev.ExitCode, ev.DroppedBefore,
			string(metadata), string(raw),
		); err != nil {
			return InsertStats{Inserted: inserted, Dropped: dropped, WriteLatency: time.Since(start)}, err
		}
		// DroppedBefore counts events lost immediately before this one, so the
		// batch total is the sum, not the last value.
		dropped += ev.DroppedBefore
		inserted++
	}
	if err := tx.Commit(); err != nil {
		return InsertStats{Inserted: inserted, Dropped: dropped, WriteLatency: time.Since(start)}, err
	}
	return InsertStats{Inserted: inserted, Dropped: dropped, WriteLatency: time.Since(start)}, nil
}

func scanEvent(row interface{ Scan(dest ...any) error }) (Event, error) {
	var ev Event
	var ts int64
	var metadata, raw string
	if err := row.Scan(
		&ev.ID, &ts, &ev.Category, &ev.Action, &ev.PID, &ev.PPID, &ev.UID, &ev.Comm, &ev.Exe,
		&ev.ContainerID, &ev.CgroupPath, &ev.Path, &ev.OldPath, &ev.LocalAddr, &ev.LocalPort,
		&ev.RemoteAddr, &ev.RemotePort, &ev.RemoteHost, &ev.TargetPID, &ev.ExitCode, &ev.DroppedBefore,
		&metadata, &raw,
	); err != nil {
		return Event{}, err
	}
	ev.SchemaVersion = SchemaVersion
	ev.Timestamp = time.Unix(0, ts).UTC()
	if metadata != "" && metadata != "{}" {
		if err := json.Unmarshal([]byte(metadata), &ev.Metadata); err != nil {
			return Event{}, err
		}
	}
	if raw != "" {
		ev.Raw = json.RawMessage(raw)
	}
	return ev, nil
}

func (s *Store) queryEvents(ctx context.Context, query string, args ...any) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MIN(ts), 0), COALESCE(MAX(ts), 0), COALESCE(SUM(dropped_before), 0) FROM events`)
	var minTS, maxTS int64
	if err := row.Scan(&status.EventCount, &minTS, &maxTS, &status.DroppedEvents); err != nil {
		return Status{}, err
	}
	if minTS != 0 {
		status.StartedAt = time.Unix(0, minTS).UTC()
	}
	if maxTS != 0 {
		status.LastEventTime = time.Unix(0, maxTS).UTC()
	}
	return status, nil
}

// LatestEventID returns the highest event id currently stored (0 if empty).
// `ltm watch` uses it as the starting cursor.
func (s *Store) LatestEventID(ctx context.Context) (int64, error) {
	var id int64
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`)
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// EventsAfterID returns events with id greater than afterID, oldest first, for
// incremental tailing.
func (s *Store) EventsAfterID(ctx context.Context, afterID int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM events WHERE id > ? ORDER BY id ASC LIMIT ?`, afterID, limit)
}

// EventsBetween returns events in [from, to], oldest first.
func (s *Store) EventsBetween(ctx context.Context, from, to time.Time, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM events WHERE ts >= ? AND ts <= ? ORDER BY ts ASC, id ASC LIMIT ?`,
		from.UnixNano(), to.UnixNano(), limit)
}

// EventsByPID returns events for a pid, oldest first.
func (s *Store) EventsByPID(ctx context.Context, pid int, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM events WHERE pid = ? ORDER BY ts ASC, id ASC LIMIT ?`, pid, limit)
}

// EventsByPath returns events touching a path (as path or old_path), oldest first.
func (s *Store) EventsByPath(ctx context.Context, path string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM events WHERE path = ? OR old_path = ? ORDER BY ts ASC, id ASC LIMIT ?`,
		path, path, limit)
}

// Query runs an arbitrary logical-AND Filter over the event log, newest first.
func (s *Store) Query(ctx context.Context, f Filter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	var where []string
	var args []any
	if !f.From.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, f.From.UnixNano())
	}
	if !f.To.IsZero() {
		where = append(where, "ts <= ?")
		args = append(args, f.To.UnixNano())
	}
	if len(f.PIDs) > 0 {
		where = append(where, "pid IN ("+placeholders(len(f.PIDs))+")")
		for _, v := range f.PIDs {
			args = append(args, v)
		}
	}
	if len(f.UIDs) > 0 {
		where = append(where, "uid IN ("+placeholders(len(f.UIDs))+")")
		for _, v := range f.UIDs {
			args = append(args, v)
		}
	}
	if len(f.Categories) > 0 {
		where = append(where, "category IN ("+placeholders(len(f.Categories))+")")
		for _, v := range f.Categories {
			args = append(args, v)
		}
	}
	if len(f.Actions) > 0 {
		where = append(where, "action IN ("+placeholders(len(f.Actions))+")")
		for _, v := range f.Actions {
			args = append(args, v)
		}
	}
	if len(f.Comms) > 0 {
		where = append(where, "comm IN ("+placeholders(len(f.Comms))+")")
		for _, v := range f.Comms {
			args = append(args, v)
		}
	}
	if f.PathLike != "" {
		where = append(where, "path LIKE ?")
		args = append(args, f.PathLike)
	}
	if f.ExeLike != "" {
		where = append(where, "exe LIKE ?")
		args = append(args, f.ExeLike)
	}

	query := `SELECT ` + eventColumns + ` FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, limit)
	return s.queryEvents(ctx, query, args...)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

var textSearchColumns = []string{
	"category", "action", "comm", "exe", "path", "old_path", "local_addr", "remote_addr", "remote_host",
	"container_id", "cgroup_path",
}

// QueryText performs an AND-of-substring free-text search across the common
// identifying columns, newest first.
func (s *Store) QueryText(ctx context.Context, terms []string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}
	terms = normalizeTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	var where []string
	var args []any
	for _, term := range terms {
		var col []string
		for _, c := range textSearchColumns {
			col = append(col, "lower("+c+") LIKE ?")
			args = append(args, "%"+term+"%")
		}
		where = append(where, "("+strings.Join(col, " OR ")+")")
	}
	query := `SELECT ` + eventColumns + ` FROM events WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	return s.queryEvents(ctx, query, args...)
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

// Sockets returns network events with an address set, newest first.
func (s *Store) Sockets(ctx context.Context, limit int) ([]SocketRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pid, comm, local_addr, local_port, remote_addr, remote_port, action, ts
		FROM events
		WHERE category = 'network' AND (local_addr != '' OR remote_addr != '')
		ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SocketRecord
	for rows.Next() {
		var sr SocketRecord
		var ts int64
		if err := rows.Scan(&sr.PID, &sr.Comm, &sr.LocalAddr, &sr.LocalPort, &sr.RemoteAddr, &sr.RemotePort, &sr.State, &ts); err != nil {
			return nil, err
		}
		sr.SeenAt = time.Unix(0, ts).UTC()
		out = append(out, sr)
	}
	return out, rows.Err()
}

// Prune deletes events older than the cutoff and reclaims space, returning
// the number of rows removed.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	if s.readOnly {
		return 0, errors.New("store opened read-only")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, cutoff.UnixNano())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return n, err
	}
	return n, nil
}

// RawSQL executes an arbitrary read-only query and returns column names plus
// rows as generic values, for `ltm sql`. Callers must use a read-only Store
// (query_only=ON) so writes fail at the SQLite layer.
func (s *Store) RawSQL(ctx context.Context, query string) ([]string, [][]any, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		out = append(out, vals)
	}
	return cols, out, rows.Err()
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
