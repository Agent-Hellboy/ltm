package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// InsertSystemSamples persists a batch of system-wide samples in one
// transaction. Column order follows systemSamplesInsertColumns (generated).
func (s *Store) InsertSystemSamples(ctx context.Context, samples []SystemSample) error {
	return s.insertBatch(ctx, `INSERT INTO system_samples (`+systemSamplesInsertColumns+`) VALUES (`+systemSamplesInsertPlaceholders+`)`,
		len(samples), func(stmt *sql.Stmt, i int) error {
			v := samples[i]
			if v.Timestamp.IsZero() {
				v.Timestamp = time.Now().UTC()
			}
			_, err := stmt.ExecContext(ctx,
				v.Timestamp.UnixNano(), v.CPUPct, v.Load1, v.Load5, v.Load15,
				v.ProcsRunning, v.ProcsBlocked, v.MemTotalKB, v.MemAvailableKB,
				v.SwapTotalKB, v.SwapFreeKB, v.PSICPUSomeAvg10, v.PSIMemSomeAvg10,
				v.PSIMemFullAvg10, v.PSIIOSomeAvg10, v.PSIIOFullAvg10,
				v.DiskReadKB, v.DiskWriteKB, v.NetRxKB, v.NetTxKB,
				v.NetRxErrs, v.NetTxErrs, v.NetRxDrop, v.NetTxDrop,
			)
			return err
		})
}

// InsertProcessSamples persists a batch of per-process samples in one
// transaction. Column order follows processSamplesInsertColumns (generated).
func (s *Store) InsertProcessSamples(ctx context.Context, samples []ProcessSample) error {
	return s.insertBatch(ctx, `INSERT INTO process_samples (`+processSamplesInsertColumns+`) VALUES (`+processSamplesInsertPlaceholders+`)`,
		len(samples), func(stmt *sql.Stmt, i int) error {
			v := samples[i]
			if v.Timestamp.IsZero() {
				v.Timestamp = time.Now().UTC()
			}
			_, err := stmt.ExecContext(ctx,
				v.Timestamp.UnixNano(), v.PID, v.Comm, v.State, v.CPUPct,
				v.RSSKB, v.Threads, v.ReadBytes, v.WriteBytes, v.Cgroup,
			)
			return err
		})
}

// insertBatch runs n prepared inserts in a single transaction. exec fills and
// executes the statement for row i. Shared by the sample writers; events keep
// their own writer because they carry drop accounting.
func (s *Store) insertBatch(ctx context.Context, query string, n int, exec func(stmt *sql.Stmt, i int) error) error {
	if n == 0 {
		return nil
	}
	if s.db == nil {
		return errors.New("store closed")
	}
	if s.readOnly {
		return errors.New("store opened read-only")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for i := range n {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := exec(stmt, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LatestSystemSample returns the most recent system sample and whether one
// exists. Backs the resource one-liner in `ltm status`.
func (s *Store) LatestSystemSample(ctx context.Context) (SystemSample, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+systemSamplesColumns+` FROM system_samples ORDER BY id DESC LIMIT 1`)
	var v SystemSample
	var ts int64
	err := row.Scan(
		&v.ID, &ts, &v.CPUPct, &v.Load1, &v.Load5, &v.Load15,
		&v.ProcsRunning, &v.ProcsBlocked, &v.MemTotalKB, &v.MemAvailableKB,
		&v.SwapTotalKB, &v.SwapFreeKB, &v.PSICPUSomeAvg10, &v.PSIMemSomeAvg10,
		&v.PSIMemFullAvg10, &v.PSIIOSomeAvg10, &v.PSIIOFullAvg10,
		&v.DiskReadKB, &v.DiskWriteKB, &v.NetRxKB, &v.NetTxKB,
		&v.NetRxErrs, &v.NetTxErrs, &v.NetRxDrop, &v.NetTxDrop,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemSample{}, false, nil
	}
	if err != nil {
		return SystemSample{}, false, err
	}
	v.Timestamp = time.Unix(0, ts).UTC()
	return v, true, nil
}
