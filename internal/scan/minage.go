package scan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/gotd/td/tg"
)

// minAgeProbeLimit bounds the age probe to a single message per chat.
const minAgeProbeLimit = 1

// minAgeProbeWorkers bounds the parallelism of age probes: enough to keep
// the single MTProto connection saturated, low enough to avoid provoking
// getHistory flood waits on large dialog sets.
const minAgeProbeWorkers = 4

// ErrUnexpectedHistoryClass reports a history variant the probe cannot read.
var ErrUnexpectedHistoryClass = errors.New("unexpected messages.getHistory result class")

// FilterByMinAge narrows targets to chats that contain at least one message
// older than the given age. Private chats expose no creation date, so the
// honest proxy is one cheap history probe per chat: messages.getHistory
// with OffsetDate set to the cutoff returns the newest message older than
// the cutoff, if any exists. Probes run on minAgeProbeWorkers workers in
// the original target order; progress (nil-safe) reports (done, total)
// after every completed probe.
func FilterByMinAge(
	ctx context.Context,
	api WalkAPI,
	targets []Target,
	age time.Duration,
	progress func(done, total int),
) ([]Target, error) {
	if age <= 0 {
		return targets, nil
	}

	cutoff := int(time.Now().Add(-age).Unix())

	results := make([]bool, len(targets))
	errs := make([]error, len(targets))

	jobs := make(chan int)

	waiter := sync.WaitGroup{}

	var done atomic.Int32

	workerCount := minAgeProbeWorkers
	if len(targets) < workerCount {
		workerCount = len(targets)
	}

	for range workerCount {
		waiter.Add(1)

		go func() {
			defer waiter.Done()

			for idx := range jobs {
				oldEnough, err := chatHasOlderMessage(ctx, api, targets[idx].InputPeer, cutoff)
				results[idx] = oldEnough
				errs[idx] = err

				if progress != nil {
					progress(int(done.Add(1)), len(targets))
				}
			}
		}()
	}

	for idx := range targets {
		jobs <- idx
	}

	close(jobs)
	waiter.Wait()

	for idx, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("probe chat %d for min age: %w", targets[idx].Chat.ID, err)
		}
	}

	kept := make([]Target, 0, len(targets))

	for idx, oldEnough := range results {
		if oldEnough {
			kept = append(kept, targets[idx])
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

	messages, err := pageMessages(page)
	if err != nil {
		return false, err
	}

	return len(messages) > 0, nil
}

// pageMessages extracts the message list of any getHistory-style result
// class; not-modified and empty pages yield no messages.
func pageMessages(page tg.MessagesMessagesClass) ([]tg.MessageClass, error) {
	switch messages := page.(type) {
	case *tg.MessagesMessages:
		return messages.Messages, nil
	case *tg.MessagesMessagesSlice:
		return messages.Messages, nil
	case *tg.MessagesChannelMessages:
		return messages.Messages, nil
	case *tg.MessagesMessagesNotModified:
		return nil, nil
	default:
		return nil, fmt.Errorf("%T: %w", page, ErrUnexpectedHistoryClass)
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
