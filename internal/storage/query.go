package storage

import (
	"context"
	"errors"
	"fmt"
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

// TopEventSources returns the processes that produced the most events since the
// given time, most active first. It backs the drop diagnostic in `ltm status`:
// when the recorder is dropping events, one process dominating recent volume is
// the signature of a runaway producer or a self-capture feedback loop, and its
// comm names the culprit.
func (s *Store) TopEventSources(ctx context.Context, since time.Time, limit int) ([]SourceCount, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT comm, COALESCE(MAX(exe), ''), COUNT(*) c
		 FROM events WHERE ts >= ?
		 GROUP BY comm ORDER BY c DESC LIMIT ?`, since.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Comm, &sc.Exe, &sc.Count); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
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

// Query runs an arbitrary logical-AND Filter over the event log. Results are
// newest first unless f.OrderAsc is set.
func (s *Store) Query(ctx context.Context, f Filter) ([]Event, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
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
		where = append(where, "(path LIKE ? OR old_path LIKE ?)")
		args = append(args, f.PathLike, f.PathLike)
	}
	if f.ExeLike != "" {
		where = append(where, "exe LIKE ?")
		args = append(args, f.ExeLike)
	}
	if f.ExactPath != "" {
		where = append(where, "(path = ? OR old_path = ?)")
		args = append(args, f.ExactPath, f.ExactPath)
	}

	query := `SELECT ` + eventColumns + ` FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if f.OrderAsc {
		query += " ORDER BY ts ASC, id ASC LIMIT ?"
	} else {
		query += " ORDER BY ts DESC, id DESC LIMIT ?"
	}
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

// maxRawSQLRows bounds how many rows RawSQL materializes in memory. Without a
// cap, a query like "SELECT * FROM events" against a long-running recorder's
// full log could allocate proportional to the entire event count.
const maxRawSQLRows = 10_000

// RawSQL executes a single arbitrary read-only statement and returns column
// names plus rows as generic values, for `ltm sql`. Callers must use a
// read-only Store (query_only=ON) so writes fail at the SQLite layer. Multiple
// statements are rejected outright: the underlying driver runs a
// semicolon-separated string as a script, so without this check an input like
// "SELECT 1; PRAGMA query_only=OFF; DELETE FROM events" would flip off the
// read-only guard mid-script and let the DELETE through on a "read-only" Store.
func (s *Store) RawSQL(ctx context.Context, query string) ([]string, [][]any, error) {
	if !s.readOnly {
		return nil, nil, errors.New("raw SQL requires read-only store")
	}
	if hasMultipleStatements(query) {
		return nil, nil, errors.New("raw SQL must be a single statement")
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
		if len(out) >= maxRawSQLRows {
			return nil, nil, fmt.Errorf("raw SQL result has more than %d rows; add a LIMIT to the query", maxRawSQLRows)
		}
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

// hasMultipleStatements reports whether sql contains a statement separator
// (';') outside quotes/comments/parens, other than a single trailing
// terminator. It walks the string once so quoted strings, bracketed
// identifiers, and comments containing ';' don't produce false positives.
func hasMultipleStatements(sql string) bool {
	depth := 0
	for i := 0; i < len(sql); {
		switch c := sql[i]; {
		case c == '\'':
			i = skipQuoted(sql, i+1, '\'')
		case c == '"':
			i = skipQuoted(sql, i+1, '"')
		case c == '[':
			i = skipUntil(sql, i+1, ']')
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			if i+1 < len(sql) {
				i += 2
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case c == ';':
			if strings.TrimSpace(sql[i+1:]) != "" {
				return true
			}
			i++
		default:
			i++
		}
	}
	return false
}

func skipQuoted(sql string, i int, quote byte) int {
	for i < len(sql) {
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(sql)
}

func skipUntil(sql string, i int, end byte) int {
	for i < len(sql) && sql[i] != end {
		i++
	}
	if i < len(sql) {
		return i + 1
	}
	return len(sql)
}
