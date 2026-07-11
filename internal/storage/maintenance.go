package storage

import (
	"context"
	"errors"
	"time"
)

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
