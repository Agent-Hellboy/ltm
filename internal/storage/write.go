package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ltm/internal/abi"
)

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
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events (`+insertColumns+`) VALUES (`+insertPlaceholders+`)`)
	if err != nil {
		return InsertStats{}, err
	}
	defer func() { _ = stmt.Close() }()

	var dropped int64
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return InsertStats{Dropped: dropped, WriteLatency: time.Since(start)}, err
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().UTC()
		}
		metadata, err := json.Marshal(ev.Metadata)
		if err != nil {
			return InsertStats{Dropped: dropped, WriteLatency: time.Since(start)}, err
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
			return InsertStats{Dropped: dropped, WriteLatency: time.Since(start)}, err
		}
		// DroppedBefore counts events lost immediately before this one, so the
		// batch total is the sum, not the last value.
		dropped += ev.DroppedBefore
	}
	if err := tx.Commit(); err != nil {
		return InsertStats{Dropped: dropped, WriteLatency: time.Since(start)}, err
	}
	return InsertStats{Dropped: dropped, WriteLatency: time.Since(start)}, nil
}

func scanEvent(rows *sql.Rows) (Event, error) {
	var ev Event
	var ts int64
	var metadata, raw string
	if err := rows.Scan(
		&ev.ID, &ts, &ev.Category, &ev.Action, &ev.PID, &ev.PPID, &ev.UID, &ev.Comm, &ev.Exe,
		&ev.ContainerID, &ev.CgroupPath, &ev.Path, &ev.OldPath, &ev.LocalAddr, &ev.LocalPort,
		&ev.RemoteAddr, &ev.RemotePort, &ev.RemoteHost, &ev.TargetPID, &ev.ExitCode, &ev.DroppedBefore,
		&metadata, &raw,
	); err != nil {
		return Event{}, err
	}
	ev.SchemaVersion = abi.SchemaVersion
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
