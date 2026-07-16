package storage

import (
	"context"
	"errors"
	"time"
)

// prunedTables lists every time-series table Prune trims by ts. The sampling
// tables (all-processes cadence) grow with box size, so they must be pruned
// alongside the event log rather than growing unbounded.
var prunedTables = []string{"events", "system_samples", "process_samples"}

// Prune deletes rows older than the cutoff from every time-series table,
// returning the total number of rows removed. VACUUM rewrites the entire
// database file and holds an exclusive lock, so it only runs when vacuum is
// true — callers doing routine/periodic pruning should leave space reclamation
// to an explicit, occasional opt-in rather than paying a full-file rewrite on
// every prune.
func (s *Store) Prune(ctx context.Context, cutoff time.Time, vacuum bool) (int64, error) {
	if s.readOnly {
		return 0, errors.New("store opened read-only")
	}
	var total int64
	for _, table := range prunedTables {
		res, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE ts < ?`, cutoff.UnixNano())
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
	}
	if total == 0 || !vacuum {
		return total, nil
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return total, err
	}
	return total, nil
}
