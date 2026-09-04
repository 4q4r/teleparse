package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion is the highest migration version this build understands.
const schemaVersion = 1

// migrationV1 creates the initial chats, runs and media schema.
const migrationV1 = `
CREATE TABLE IF NOT EXISTS chats (
    chat_id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    title TEXT,
    username TEXT,
    last_seen_message_id INTEGER NOT NULL DEFAULT 0,
    last_synced_at TEXT
);

CREATE TABLE IF NOT EXISTS runs (
    run_id TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    profile TEXT,
    filter_json TEXT NOT NULL,
    status TEXT NOT NULL,
    resume_at TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    error TEXT
);

CREATE TABLE IF NOT EXISTS media (
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    media_index INTEGER NOT NULL DEFAULT 0,
    media_class TEXT NOT NULL,
    media_id INTEGER NOT NULL,
    mime TEXT,
    size INTEGER,
    date TEXT,
    sender_id INTEGER,
    filename TEXT,
    grouped_id INTEGER,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    path TEXT,
    bytes_done INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(chat_id, message_id, media_index)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_media_file ON media(media_class, media_id);
CREATE INDEX IF NOT EXISTS idx_media_pending ON media(status) WHERE status IN ('queued', 'failed');
`

// migrate creates the migrations table when missing and applies every pending
// migration batch inside one transaction.
func (s *Store) migrate() error {
	ctx := context.Background()

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}

	if err := applyMigrations(ctx, tx); err != nil {
		_ = tx.Rollback()

		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}

	return nil
}

// applyMigrations upgrades tx to schemaVersion, recording each applied batch.
func applyMigrations(ctx context.Context, tx *sql.Tx) error {
	var applied int

	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if applied > schemaVersion {
		return fmt.Errorf("schema v%d: %w", applied, ErrSchemaAhead)
	}

	for version := applied; version < schemaVersion; version++ {
		if err := applyMigration(ctx, tx, version); err != nil {
			return err
		}
	}

	return nil
}

// applyMigration runs the single DDL batch upgrading version to version+1.
func applyMigration(ctx context.Context, tx *sql.Tx, version int) error {
	batch, ok := migrationFor(version)
	if !ok {
		return fmt.Errorf("no migration from v%d: %w", version, ErrSchemaAhead)
	}

	if _, err := tx.ExecContext(ctx, batch); err != nil {
		return fmt.Errorf("apply migration v%d: %w", version+1, err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO migrations (version, applied_at) VALUES (?, ?)`,
		version+1, nowUTC()); err != nil {
		return fmt.Errorf("record migration v%d: %w", version+1, err)
	}

	return nil
}

// migrationFor returns the DDL batch upgrading version to version+1.
func migrationFor(version int) (string, bool) {
	if version == 0 {
		return migrationV1, true
	}

	return "", false
}
