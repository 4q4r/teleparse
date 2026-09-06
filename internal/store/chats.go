package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Chat is a dialog tracked for incremental-sync watermarks.
type Chat struct {
	ChatID   int64
	Type     string
	Title    *string
	Username *string
}

// UpsertChat inserts or refreshes chat metadata without touching the
// watermark columns, so discovery re-runs never lose sync progress.
func (s *Store) UpsertChat(ctx context.Context, chat Chat) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chats (chat_id, type, title, username)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			type = excluded.type,
			title = excluded.title,
			username = excluded.username`,
		chat.ChatID, chat.Type, chat.Title, chat.Username)
	if err != nil {
		return fmt.Errorf("upsert chat %d: %w", chat.ChatID, err)
	}

	return nil
}

// Watermark returns the highest contiguous processed message id for the
// chat, or zero when the chat is unknown.
func (s *Store) Watermark(ctx context.Context, chatID int64) (int64, error) {
	var watermark int64

	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT last_seen_message_id FROM chats WHERE chat_id = ?), 0)`,
		chatID).Scan(&watermark)
	if err != nil {
		return 0, fmt.Errorf("watermark chat %d: %w", chatID, err)
	}

	return watermark, nil
}

// ChatLastWalked returns when the chat last completed a walk, as stamped by
// AdvanceWatermark; ok is false when the chat has never been walked.
func (s *Store) ChatLastWalked(ctx context.Context, chatID int64) (time.Time, bool, error) {
	var stamped *string

	err := s.db.QueryRowContext(ctx, `
		SELECT last_synced_at FROM chats WHERE chat_id = ?`, chatID).Scan(&stamped)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}

		return time.Time{}, false, fmt.Errorf("last walked chat %d: %w", chatID, err)
	}

	if stamped == nil {
		return time.Time{}, false, nil
	}

	when, err := time.Parse(time.RFC3339Nano, *stamped)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse last_synced_at chat %d: %w", chatID, err)
	}

	return when, true, nil
}

// AdvanceWatermark moves the chat watermark forward to maxContiguousID and
// stamps last_synced_at with when; lower ids never regress the watermark,
// but every completed walk still refreshes the stamp so walk freshness
// survives chats whose history stood still.
func (s *Store) AdvanceWatermark(ctx context.Context, chatID int64, maxContiguousID int64, when time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE chats
		SET last_seen_message_id = MAX(last_seen_message_id, ?),
		    last_synced_at = ?
		WHERE chat_id = ?`,
		maxContiguousID, formatTime(when), chatID)
	if err != nil {
		return fmt.Errorf("advance watermark chat %d: %w", chatID, err)
	}

	return nil
}
