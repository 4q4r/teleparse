package store

import (
	"context"
	"fmt"
)

// ResetWatermark clears the chat's watermark to zero so the next walk
// re-processes the full history; the chat's other metadata stays intact.
func (s *Store) ResetWatermark(ctx context.Context, chatID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE chats
		SET last_seen_message_id = 0, last_synced_at = ?
		WHERE chat_id = ?`, nowUTC(), chatID)
	if err != nil {
		return fmt.Errorf("reset watermark chat %d: %w", chatID, err)
	}

	return nil
}

// CachedMatched counts the chat's manifest rows that previous runs already
// matched: every status except failed (a failed row matched too, but its
// match was not consummated, so counts must not claim it).
func (s *Store) CachedMatched(ctx context.Context, chatID int64) (int, error) {
	var cached int

	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM media
		WHERE chat_id = ? AND status != ?`, chatID, StatusFailed).Scan(&cached)
	if err != nil {
		return 0, fmt.Errorf("count cached chat %d: %w", chatID, err)
	}

	return cached, nil
}

// PendingMedia returns the chat's manifest rows that still owe a download
// (discovered, queued or failed), ordered by message id; done and skipped
// rows are final and never re-offered.
func (s *Store) PendingMedia(ctx context.Context, chatID int64) ([]*MediaItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT`+mediaColumns+`
		FROM media
		WHERE chat_id = ? AND status IN (?, ?, ?)
		ORDER BY message_id, media_index`,
		chatID, StatusDiscovered, StatusQueued, StatusFailed)
	if err != nil {
		return nil, fmt.Errorf("select pending media chat %d: %w", chatID, err)
	}

	defer func() { _ = rows.Close() }()

	var items []*MediaItem

	for rows.Next() {
		item, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending media row: %w", err)
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending media: %w", err)
	}

	return items, nil
}
