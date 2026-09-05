package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// mediaColumns lists every media column in scan order.
const mediaColumns = `
	chat_id, message_id, media_index, media_class, media_id,
	mime, size, date, sender_id, filename, grouped_id,
	status, attempts, last_error, path, bytes_done, sha256, updated_at`

// MediaItem is one tracked media entity: discovery metadata plus mutable
// download progress.
type MediaItem struct {
	ChatID     int64
	MessageID  int64
	MediaIndex int
	MediaClass string
	MediaID    int64
	Mime       *string
	Size       *int64
	Date       *string
	SenderID   *int64
	Filename   *string
	GroupedID  *int64
	Status     string
	Attempts   int
	LastError  *string
	Path       *string
	BytesDone  int64
	Sha256     *string
	UpdatedAt  string
}

// MediaRow is the flat, JSON-tagged media projection consumed by the export
// writers.
type MediaRow struct {
	ChatID     int64   `json:"chat_id"`
	MessageID  int64   `json:"message_id"`
	MediaIndex int     `json:"media_index"`
	MediaClass string  `json:"media_class"`
	MediaID    int64   `json:"media_id"`
	Mime       *string `json:"mime,omitempty"`
	Size       *int64  `json:"size,omitempty"`
	Date       *string `json:"date,omitempty"`
	SenderID   *int64  `json:"sender_id,omitempty"`
	Filename   *string `json:"filename,omitempty"`
	GroupedID  *int64  `json:"grouped_id,omitempty"`
	Status     string  `json:"status"`
	Path       *string `json:"path,omitempty"`
	Sha256     *string `json:"sha256,omitempty"`
}

// ChatStats aggregates completed downloads per chat.
type ChatStats struct {
	ChatID    int64
	Title     string
	DoneCount int64
	DoneBytes int64
}

// UpsertMedia inserts a media row or, when the same (chat, message, index)
// already exists, updates only mutable progress: status, attempts,
// last_error, path, bytes_done, sha256 and updated_at. Discovery metadata
// stays as first recorded. When a different message already tracks the same
// unique (media_class, media_id) file — the same Telegram file forwarded
// into another chat — nothing is written and stored reports false: the
// first occurrence wins, so re-walks stay idempotent instead of aborting
// the run. Both conflicts are resolved inside one statement, which makes
// the duplicate outcome race-free by construction.
func (s *Store) UpsertMedia(ctx context.Context, item *MediaItem) (bool, error) {
	item.UpdatedAt = nowUTC()

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO media (`+mediaColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, message_id, media_index) DO UPDATE SET
			status = excluded.status,
			attempts = excluded.attempts,
			last_error = excluded.last_error,
			path = excluded.path,
			bytes_done = excluded.bytes_done,
			sha256 = excluded.sha256,
			updated_at = excluded.updated_at
		ON CONFLICT(media_class, media_id) DO NOTHING`,
		item.ChatID, item.MessageID, item.MediaIndex, item.MediaClass, item.MediaID,
		item.Mime, item.Size, item.Date, item.SenderID, item.Filename, item.GroupedID,
		item.Status, item.Attempts, item.LastError, item.Path, item.BytesDone, item.Sha256,
		item.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("upsert media %d/%d/%d: %w", item.ChatID, item.MessageID, item.MediaIndex, err)
	}

	changed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count upserted media %d/%d/%d: %w",
			item.ChatID, item.MessageID, item.MediaIndex, err)
	}

	return changed > 0, nil
}

// MediaByFile returns the media row owning the unique (class, media_id)
// file and whether it exists.
func (s *Store) MediaByFile(ctx context.Context, class string, mediaID int64) (*MediaItem, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT`+mediaColumns+`
		FROM media
		WHERE media_class = ? AND media_id = ?`, class, mediaID)

	item, err := scanMedia(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("media by file %s/%d: %w", class, mediaID, err)
	}

	return item, true, nil
}

// ClaimPending atomically moves up to limit queued or retryable failed rows
// of the chat into downloading and returns them ordered by message; failed
// rows are reclaimable only while attempts stays below maxAttempts.
func (s *Store) ClaimPending(ctx context.Context, chatID int64, limit int, maxAttempts int) ([]*MediaItem, error) {
	var items []*MediaItem

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT`+mediaColumns+`
			FROM media
			WHERE chat_id = ? AND status IN (?, ?) AND attempts < ?
			ORDER BY message_id, media_index
			LIMIT ?`, chatID, StatusQueued, StatusFailed, maxAttempts, limit)
		if err != nil {
			return fmt.Errorf("select pending chat %d: %w", chatID, err)
		}

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			item, err := scanMedia(rows)
			if err != nil {
				return fmt.Errorf("scan pending row: %w", err)
			}

			items = append(items, item)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate pending: %w", err)
		}

		stmt, err := tx.PrepareContext(ctx, `
			UPDATE media SET status = ?, updated_at = ?
			WHERE chat_id = ? AND message_id = ? AND media_index = ?`)
		if err != nil {
			return fmt.Errorf("prepare claim: %w", err)
		}

		defer func() { _ = stmt.Close() }()

		for _, item := range items {
			if _, err := stmt.ExecContext(ctx, StatusDownloading, nowUTC(),
				item.ChatID, item.MessageID, item.MediaIndex); err != nil {
				return fmt.Errorf("claim %d/%d/%d: %w",
					item.ChatID, item.MessageID, item.MediaIndex, err)
			}

			item.Status = StatusDownloading
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return items, nil
}

// MarkDone completes a media row with its final path and optional sha256.
func (s *Store) MarkDone(ctx context.Context, chatID, messageID int64, mediaIndex int,
	path string, sha256 *string,
) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE media
		SET status = ?, path = ?, sha256 = ?, updated_at = ?
		WHERE chat_id = ? AND message_id = ? AND media_index = ?`,
		StatusDone, path, sha256, nowUTC(), chatID, messageID, mediaIndex)
	if err != nil {
		return fmt.Errorf("mark done %d/%d/%d: %w", chatID, messageID, mediaIndex, err)
	}

	return nil
}

// RecordAttempt bumps the attempt counter and last error of a claimed row
// while keeping its downloading status, so the in-flight retry ladder keeps
// ownership and ClaimPending cannot hand the row to a second worker.
func (s *Store) RecordAttempt(ctx context.Context, chatID, messageID int64, mediaIndex int,
	errText string, attempts int,
) error {
	var errPtr *string

	if errText != "" {
		errPtr = &errText
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE media
		SET last_error = ?, attempts = ?, updated_at = ?
		WHERE chat_id = ? AND message_id = ? AND media_index = ?`,
		errPtr, attempts, nowUTC(), chatID, messageID, mediaIndex)
	if err != nil {
		return fmt.Errorf("record attempt %d/%d/%d: %w", chatID, messageID, mediaIndex, err)
	}

	return nil
}

// MarkFailed records a failed attempt with its error text and attempt count.
func (s *Store) MarkFailed(ctx context.Context, chatID, messageID int64, mediaIndex int,
	errText string, attempts int,
) error {
	var errPtr *string

	if errText != "" {
		errPtr = &errText
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE media
		SET status = ?, last_error = ?, attempts = ?, updated_at = ?
		WHERE chat_id = ? AND message_id = ? AND media_index = ?`,
		StatusFailed, errPtr, attempts, nowUTC(), chatID, messageID, mediaIndex)
	if err != nil {
		return fmt.Errorf("mark failed %d/%d/%d: %w", chatID, messageID, mediaIndex, err)
	}

	return nil
}

// MarkInterrupted records a cancellation-interrupted transfer as still
// owned by its run (status stays downloading, so the live feeder cannot
// re-claim it) with its retry budget restored: attempts reset to zero
// because a cancellation — user interrupt, park or dropped connection — is
// never chargeable to the item. The next run reclaims the row through
// ResetDownloading exactly like crash recovery.
func (s *Store) MarkInterrupted(ctx context.Context, chatID, messageID int64, mediaIndex int,
	errText string,
) error {
	var errPtr *string

	if errText != "" {
		errPtr = &errText
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE media
		SET status = ?, last_error = ?, attempts = ?, updated_at = ?
		WHERE chat_id = ? AND message_id = ? AND media_index = ?`,
		StatusDownloading, errPtr, 0, nowUTC(), chatID, messageID, mediaIndex)
	if err != nil {
		return fmt.Errorf("mark interrupted %d/%d/%d: %w", chatID, messageID, mediaIndex, err)
	}

	return nil
}

// Counts returns the number of media rows per status, including zeroed
// entries for every media lifecycle status.
func (s *Store) Counts(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{
		StatusDiscovered:  0,
		StatusQueued:      0,
		StatusDownloading: 0,
		StatusDone:        0,
		StatusFailed:      0,
		StatusSkipped:     0,
	}

	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM media GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count media: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			status string
			count  int
		)

		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan count row: %w", err)
		}

		counts[status] = count
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate counts: %w", err)
	}

	return counts, nil
}

// StatsPerChat returns per-chat done counts and summed done sizes ordered
// by chat id; chats without a chats row report an empty title.
func (s *Store) StatsPerChat(ctx context.Context) ([]ChatStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.chat_id, COALESCE(c.title, ''), COUNT(*), COALESCE(SUM(m.size), 0)
		FROM media m
		LEFT JOIN chats c ON c.chat_id = m.chat_id
		WHERE m.status = ?
		GROUP BY m.chat_id
		ORDER BY m.chat_id`, StatusDone)
	if err != nil {
		return nil, fmt.Errorf("stats per chat: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var stats []ChatStats

	for rows.Next() {
		var row ChatStats

		if err := rows.Scan(&row.ChatID, &row.Title, &row.DoneCount, &row.DoneBytes); err != nil {
			return nil, fmt.Errorf("scan stats row: %w", err)
		}

		stats = append(stats, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stats: %w", err)
	}

	return stats, nil
}

// ResetDownloading requeues rows stranded in downloading by a crash and
// returns how many were reset.
func (s *Store) ResetDownloading(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE media SET status = ?, updated_at = ? WHERE status = ?`,
		StatusQueued, nowUTC(), StatusDownloading)
	if err != nil {
		return 0, fmt.Errorf("reset downloading: %w", err)
	}

	reset, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count reset rows: %w", err)
	}

	return reset, nil
}

// ListMediaRows returns export rows ordered by chat, message and index; a
// non-zero chatID scopes the listing to a single chat.
func (s *Store) ListMediaRows(ctx context.Context, chatID int64) ([]MediaRow, error) {
	query := `SELECT` + mediaColumns + `
		FROM media`
	args := []any{}

	if chatID != 0 {
		query += `
		WHERE chat_id = ?`

		args = append(args, chatID)
	}

	query += `
		ORDER BY chat_id, message_id, media_index`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list media rows: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []MediaRow

	for rows.Next() {
		item, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan media row: %w", err)
		}

		out = append(out, rowFromItem(item))
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media rows: %w", err)
	}

	return out, nil
}

// scanMedia hydrates a MediaItem from one query row.
func scanMedia(row scanner) (*MediaItem, error) {
	var item MediaItem

	err := row.Scan(&item.ChatID, &item.MessageID, &item.MediaIndex, &item.MediaClass, &item.MediaID,
		&item.Mime, &item.Size, &item.Date, &item.SenderID, &item.Filename, &item.GroupedID,
		&item.Status, &item.Attempts, &item.LastError, &item.Path, &item.BytesDone, &item.Sha256,
		&item.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan media row: %w", err)
	}

	return &item, nil
}

// rowFromItem projects a MediaItem into the flat export shape.
func rowFromItem(item *MediaItem) MediaRow {
	return MediaRow{
		ChatID:     item.ChatID,
		MessageID:  item.MessageID,
		MediaIndex: item.MediaIndex,
		MediaClass: item.MediaClass,
		MediaID:    item.MediaID,
		Mime:       item.Mime,
		Size:       item.Size,
		Date:       item.Date,
		SenderID:   item.SenderID,
		Filename:   item.Filename,
		GroupedID:  item.GroupedID,
		Status:     item.Status,
		Path:       item.Path,
		Sha256:     item.Sha256,
	}
}
