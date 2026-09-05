package scan

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/gotd/td/tg"
)

// minAgeProbeLimit bounds the age probe to a single message per chat.
const minAgeProbeLimit = 1

// ErrUnexpectedHistoryClass reports a history variant the probe cannot read.
var ErrUnexpectedHistoryClass = errors.New("unexpected messages.getHistory result class")

// FilterByMinAge narrows targets to chats that contain at least one message
// older than the given age. Private chats expose no creation date, so the
// honest proxy is one cheap history probe per chat: messages.getHistory
// with OffsetDate set to the cutoff returns the newest message older than
// the cutoff, if any exists.
func FilterByMinAge(ctx context.Context, api WalkAPI, targets []Target, age time.Duration) ([]Target, error) {
	if age <= 0 {
		return targets, nil
	}

	cutoff := int(time.Now().Add(-age).Unix())

	kept := make([]Target, 0, len(targets))

	for _, target := range targets {
		oldEnough, err := chatHasOlderMessage(ctx, api, target.InputPeer, cutoff)
		if err != nil {
			return nil, fmt.Errorf("probe chat %d for min age: %w", target.Chat.ID, err)
		}

		if oldEnough {
			kept = append(kept, target)
		}
	}

	return kept, nil
}

// chatHasOlderMessage reports whether the peer has at least one message
// strictly older than the cutoff unix timestamp.
func chatHasOlderMessage(ctx context.Context, api WalkAPI, peer tg.InputPeerClass, cutoff int) (bool, error) {
	page, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:       peer,
		OffsetDate: cutoff,
		Limit:      minAgeProbeLimit,
	})
	if err != nil {
		return false, err //nolint:wrapcheck // caller wraps with chat context
	}

	switch messages := page.(type) {
	case *tg.MessagesMessages:
		return len(messages.Messages) > 0, nil
	case *tg.MessagesMessagesSlice:
		return len(messages.Messages) > 0, nil
	case *tg.MessagesChannelMessages:
		return len(messages.Messages) > 0, nil
	case *tg.MessagesMessagesNotModified:
		return false, nil
	default:
		return false, fmt.Errorf("%T: %w", page, ErrUnexpectedHistoryClass)
	}
}

// MinAgeFromOptions resolves the chat-min-age filter into a duration;
// zero when unset.
func MinAgeFromOptions(opts filters.Options) (time.Duration, error) {
	if opts.ChatMinAge == "" {
		return 0, nil
	}

	d, err := filters.ParseRelative(opts.ChatMinAge)
	if err != nil {
		return 0, fmt.Errorf("chat-min-age %q: %w: %w", opts.ChatMinAge, filters.ErrBadParse, err)
	}

	return d, nil
}
