package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// noLimit is SQLite's LIMIT value meaning unbounded.
const noLimit = -1

// Run records one scan/download execution for resume and bookkeeping.
type Run struct {
	RunID      string
	Account    string
	Profile    *string
	FilterJSON string
	Status     string
	ResumeAt   *string
	StartedAt  string
	FinishedAt *string
	Error      *string
}

// CreateRun inserts run in the running state and stamps StartedAt.
func (s *Store) CreateRun(ctx context.Context, run *Run) error {
	run.Status = StatusRunning
	run.StartedAt = nowUTC()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (run_id, account, profile, filter_json, status, resume_at, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.RunID, run.Account, run.Profile, run.FilterJSON, run.Status, run.ResumeAt, run.StartedAt)
	if err != nil {
		return fmt.Errorf("create run %s: %w", run.RunID, err)
	}

	return nil
}

// FinishRun stamps the run's final status and finish time; a non-empty
// errText is recorded while empty text clears the error column.
func (s *Store) FinishRun(ctx context.Context, runID string, status string, errText string) error {
	var errPtr *string

	if errText != "" {
		errPtr = &errText
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE runs SET status = ?, finished_at = ?, error = ? WHERE run_id = ?`,
		status, nowUTC(), errPtr, runID)
	if err != nil {
		return fmt.Errorf("finish run %s: %w", runID, err)
	}

	return nil
}

// SetResumeAt records when a parked run should be resumed.
func (s *Store) SetResumeAt(ctx context.Context, runID string, when time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE runs SET resume_at = ? WHERE run_id = ?`,
		formatTime(when), runID)
	if err != nil {
		return fmt.Errorf("set resume_at for run %s: %w", runID, err)
	}

	return nil
}

// ListRuns returns up to limit runs newest first; a non-positive limit
// returns every run.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = noLimit
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, account, profile, filter_json, status, resume_at, started_at, finished_at, error
		FROM runs
		ORDER BY started_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var runs []Run

	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs row: %w", err)
		}

		runs = append(runs, *run)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}

	return runs, nil
}

// GetRun returns the run by id and whether it exists.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, account, profile, filter_json, status, resume_at, started_at, finished_at, error
		FROM runs
		WHERE run_id = ?`, runID)

	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("get run %s: %w", runID, err)
	}

	return run, true, nil
}

// CleanupFinishedRuns deletes done or failed runs whose finish time predates
// olderThan and returns the number of removed rows.
func (s *Store) CleanupFinishedRuns(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM runs
		WHERE status IN (?, ?) AND finished_at IS NOT NULL AND finished_at < ?`,
		StatusDone, StatusFailed, formatTime(olderThan))
	if err != nil {
		return 0, fmt.Errorf("cleanup finished runs: %w", err)
	}

	removed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count cleaned runs: %w", err)
	}

	return removed, nil
}

// scanRun hydrates a Run from one query row.
func scanRun(row scanner) (*Run, error) {
	var run Run

	err := row.Scan(&run.RunID, &run.Account, &run.Profile, &run.FilterJSON, &run.Status,
		&run.ResumeAt, &run.StartedAt, &run.FinishedAt, &run.Error)
	if err != nil {
		return nil, fmt.Errorf("scan run row: %w", err)
	}

	return &run, nil
}
