package tg

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram"
	gotdtg "github.com/gotd/td/tg"
)

// dialogCountLimit bounds the dialog-count probe: one page, one dialog.
const dialogCountLimit = 1

// ErrUnexpectedDialogsClass reports a dialogs variant the counter cannot read.
var ErrUnexpectedDialogsClass = errors.New("unexpected messages.getDialogs result class")

// DialogCount returns the total dialog count visible to the account in a
// single cheap RPC (messages.getDialogs with Limit=1 answers Count in the
// slice header). It feeds the auto-takeout size heuristic.
func DialogCount(ctx context.Context, client *telegram.Client) (int, error) {
	result, err := client.API().MessagesGetDialogs(ctx, &gotdtg.MessagesGetDialogsRequest{
		Limit:      dialogCountLimit,
		OffsetPeer: &gotdtg.InputPeerEmpty{},
	})
	if err != nil {
		return 0, fmt.Errorf("count dialogs: %w", err)
	}

	switch page := result.(type) {
	case *gotdtg.MessagesDialogsSlice:
		return page.Count, nil
	case *gotdtg.MessagesDialogs:
		// messages.dialogs carries no count: the whole set fit one page.
		return len(page.Dialogs), nil
	default:
		return 0, fmt.Errorf("%T: %w", result, ErrUnexpectedDialogsClass)
	}
}
