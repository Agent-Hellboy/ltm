package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schemaStatements, eventColumns, insertColumns and insertPlaceholders are
// generated from internal/abi/abi.yaml into schema_gen.go — do not add them
// here by hand.

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
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve storage path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	// sql.Open doesn't establish a connection by itself; Ping forces one now
	// so a bad DSN or permission problem fails here instead of on first query.
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	for _, stmt := range schemaStatements {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
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
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve storage path: %w", err)
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
	// sql.Open doesn't establish a connection by itself; Ping forces one now
	// so a bad DSN or permission problem fails here instead of on first query.
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{db: db, readOnly: true}, nil
}

func dsn(path string, readOnly bool) string {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout(5000)")
	values.Add("_pragma", "journal_mode(wal)")
	values.Add("_pragma", "synchronous(normal)")
	if readOnly {
		values.Add("_pragma", "query_only(true)")
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
