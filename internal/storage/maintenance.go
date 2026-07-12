package storage

import (
	"context"
	"errors"
	"time"
)

// Prune deletes events older than the cutoff, returning the number of rows
// removed. VACUUM rewrites the entire database file and holds an exclusive
// lock, so it only runs when vacuum is true — callers doing routine/periodic
// pruning should leave space reclamation to an explicit, occasional opt-in
// rather than paying a full-file rewrite on every prune.
func (s *Store) Prune(ctx context.Context, cutoff time.Time, vacuum bool) (int64, error) {
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
	if n == 0 || !vacuum {
		return n, nil
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return n, err
	}
	return n, nil
}
