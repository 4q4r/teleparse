package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
)

// ErrNoFetch reports a Manager run without a configured FetchFunc.
var ErrNoFetch = errors.New("no fetch function configured")

// ErrNoLocation reports a download attempt without a usable file location.
var ErrNoLocation = errors.New("no file location for media item")

// ErrMessageGone reports a refetch whose source message no longer exists
// (deleted or returned empty); no retry can recover the item.
var ErrMessageGone = errors.New("source message gone")

// ErrMediaGone reports a refetched message whose media is no longer
// downloadable; no retry can recover the item.
var ErrMediaGone = errors.New("message media gone")

// ErrFileTooBigForTakeout reports a file exceeding the active takeout
// session's per-account size cap (2GiB, raised to 4GiB by Telegram
// Premium); no retry inside the session can recover it.
var ErrFileTooBigForTakeout = errors.New(
	"file too big for the takeout session (2GiB cap; Telegram Premium raises it to 4GiB)")

// RefetchFunc rebuilds the file location by refetching the source message:
// when the stored file_reference has expired, or when an item carries no
// location at all because its walk context is gone (cached manifest rows).
type RefetchFunc func(ctx context.Context) (tg.InputFileLocationClass, error)

// Input is one ranged download request: the media identity, the byte offset
// the destination already holds, the current location, the data center the
// file lives on (0 when unknown) and an optional refetch hook that mints a
// fresh location for FILE_REFERENCE_EXPIRED handling and for items whose
// location is only obtainable by refetching the source message.
type Input struct {
	Item     store.MediaItem
	Offset   int64
	Location tg.InputFileLocationClass
	DC       int
	Refetch  RefetchFunc
}

// FetchFunc transfers the bytes [Input.Offset, file end) into dest via
// WriteAt at absolute offsets. It must not write below Input.Offset.
type FetchFunc func(ctx context.Context, in Input, dest io.WriterAt) (int64, error)

// fileExists reports whether path is present on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// InvokerSource supplies RPC targets bound to a data center. Production is
// tg.DownloadPools: media-only connection pools per DC with a home-DC
// fallback; dc 0 (unknown) maps to the home pool.
type InvokerSource interface {
	InvokerFor(ctx context.Context, dc int) (downloader.Client, error)
}

// errTypeFileMigrate is the RPC error type carrying the file's real DC in
// its argument (code 303, core.telegram.org/mtproto/service_messages).
const errTypeFileMigrate = "FILE_MIGRATE"

// invokerFor resolves the RPC target: the DC pool when pools knows the DC,
// the fallback primary connection otherwise. Pool creation failures degrade
// to the fallback rather than failing the transfer.
//
//nolint:ireturn // the downloader.Client union is the gotd download contract
func invokerFor(ctx context.Context, pools InvokerSource, fallback downloader.Client, dc int) downloader.Client {
	if pools == nil {
		return fallback
	}

	rpc, err := pools.InvokerFor(ctx, dc)
	if err != nil || rpc == nil {
		return fallback
	}

	return rpc
}

// resolveLocation returns the request's file location, refetching the source
// message when the stored reference is absent.
//
//nolint:ireturn // the gotd location union is the downloader input contract
func resolveLocation(ctx context.Context, input Input) (tg.InputFileLocationClass, error) {
	location := input.Location

	if location == nil && input.Refetch != nil {
		fresh, err := input.Refetch(ctx)
		if err != nil {
			return nil, fmt.Errorf("refetch location for %d/%d: %w", input.Item.ChatID, input.Item.MessageID, err)
		}

		location = fresh
	}

	if location == nil {
		return nil, fmt.Errorf("media %s/%d: %w", input.Item.MediaClass, input.Item.MediaID, ErrNoLocation)
	}

	return location, nil
}
