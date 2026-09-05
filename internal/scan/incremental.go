package scan

import (
	"context"
	"fmt"

	tg "github.com/gotd/td/tg"
)

// newestProbeLimit bounds the newest-id probe to a single message per chat.
const newestProbeLimit = 1

// ShouldWalkIncrementally reports whether a chat with the stored watermark
// can be walked incrementally given its current newest message id: the
// watermark must be positive and reached by the newest id, so only messages
// past the watermark remain to walk.
func ShouldWalkIncrementally(storedWatermark, newestID int64) bool {
	return storedWatermark > 0 && newestID >= storedWatermark
}

// HistoryCleared reports whether the chat's history was mass-cleared since
// the last walk: the newest message id dropped strictly below the stored
// watermark, which ordinary message flow can never do.
func HistoryCleared(storedWatermark, newestID int64) bool {
	return newestID < storedWatermark
}

// NewestMessageID returns the peer's newest message id via one cheap
// messages.getHistory call (limit 1; the server returns newest first). Zero
// means the chat holds no readable message.
func NewestMessageID(ctx context.Context, api WalkAPI, peer tg.InputPeerClass) (int64, error) {
	page, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  peer,
		Limit: newestProbeLimit,
	})
	if err != nil {
		return 0, fmt.Errorf("probe newest message: %w", err)
	}

	messages, err := pageMessages(page)
	if err != nil {
		return 0, fmt.Errorf("probe newest message: %w", err)
	}

	if len(messages) == 0 {
		return 0, nil
	}

	switch typed := messages[0].(type) {
	case *tg.Message:
		return int64(typed.ID), nil
	case *tg.MessageService:
		return int64(typed.ID), nil
	default:
		return 0, fmt.Errorf("%T: %w", messages[0], ErrUnexpectedHistoryClass)
	}
}
