// Package store persists teleparse state in SQLite: chat watermarks for
// incremental syncs, run bookkeeping for resume, and the media manifest that
// drives downloads, deduplication and crash recovery.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary CGO-free
)

// busyTimeoutMs is the SQLite lock wait applied to every pool connection.
const busyTimeoutMs = 5000

// Sentinel errors wrapped by dynamic Store failures.
var (
	// ErrDuplicateFile reports that a different message already tracks the
	// same unique media file id.
	ErrDuplicateFile = errors.New("file already tracked under another message")
	// ErrSchemaAhead reports a database written by a newer teleparse build.
	ErrSchemaAhead = errors.New("database schema is newer than this build")
)

// Lifecycle status values stored in media.status and runs.status: runs use
// running, parked, done and failed; media use discovered, queued,
// downloading, done, failed and skipped.
const (
	StatusDiscovered  = "discovered"
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusParked      = "parked"
	StatusDownloading = "downloading"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusSkipped     = "skipped"
)

// Store wraps the state database. The pool serves a single connection so
// multi-statement transactions serialize read-modify-write sequences.
type Store struct {
	db *sql.DB
}

// scanner is the shared row-scanning surface of *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// Open opens or creates the state database at path, enabling WAL journaling,
// a five-second busy timeout and foreign keys, then applies pending
// migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		path, busyTimeoutMs)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	out := &Store{db: db}

	if err := out.migrate(); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migrate state db %s: %w", path, err)
	}

	return out, nil
}

// Close releases the state database connection pool.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state db: %w", err)
	}

	return nil
}

// withTx runs body inside a transaction and rolls back when it fails.
func (s *Store) withTx(ctx context.Context, body func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if err := body(tx); err != nil {
		_ = tx.Rollback()

		return fmt.Errorf("run tx: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	return nil
}

// nowUTC stamps a UTC timestamp in the shared TEXT-column format.
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// formatTime renders when for nullable TEXT columns; zero times become NULL.
func formatTime(when time.Time) *string {
	if when.IsZero() {
		return nil
	}

	stamped := when.UTC().Format(time.RFC3339Nano)

	return &stamped
}
