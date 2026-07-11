package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

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
		pattern := "%" + escapeLike(term) + "%"
		var col []string
		for _, c := range textSearchColumns {
			col = append(col, "lower("+c+") LIKE ? ESCAPE '\\'")
			args = append(args, pattern)
		}
		where = append(where, "("+strings.Join(col, " OR ")+")")
	}
	query := `SELECT ` + eventColumns + ` FROM events WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	return s.queryEvents(ctx, query, args...)
}

// escapeLike neutralizes the LIKE metacharacters so a search term containing
// %, _, or \ matches literally (paired with `ESCAPE '\'`).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
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
		SELECT pid, comm, local_port, action, ts
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
		if err := rows.Scan(&sr.PID, &sr.Comm, &sr.LocalPort, &sr.State, &ts); err != nil {
			return nil, err
		}
		sr.SeenAt = time.Unix(0, ts).UTC()
		out = append(out, sr)
	}
	return out, rows.Err()
}

// RawSQL executes an arbitrary read-only query and returns column names plus
// rows as generic values, for `ltm sql`. Callers must use a read-only Store
// (query_only=ON) so writes fail at the SQLite layer.
func (s *Store) RawSQL(ctx context.Context, query string) ([]string, [][]any, error) {
	if !s.readOnly {
		return nil, nil, errors.New("raw SQL requires read-only store")
	}
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
