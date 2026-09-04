package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"teleparse/internal/store"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
)

// ErrNoFetch reports a Manager run without a configured FetchFunc.
var ErrNoFetch = errors.New("no fetch function configured")

// ErrNoLocation reports a download attempt without a usable file location.
var ErrNoLocation = errors.New("no file location for media item")

// RefetchFunc rebuilds the file location, refetching the source message when
// the stored file_reference has expired.
type RefetchFunc func(ctx context.Context) (tg.InputFileLocationClass, error)

// Input is one ranged download request: the media identity, the byte offset
// the destination already holds, the current location and an optional
// refetch hook for FILE_REFERENCE_EXPIRED handling.
type Input struct {
	Item     store.MediaItem
	Offset   int64
	Location tg.InputFileLocationClass
	Refetch  RefetchFunc
}

// FetchFunc transfers the bytes [Input.Offset, file end) into dest via
// WriteAt at absolute offsets. It must not write below Input.Offset;
// production implementations that cannot range-request wrap dest in
// NewSkipWriterAt instead.
type FetchFunc func(ctx context.Context, in Input, dest io.WriterAt) (int64, error)

// GotdFetch adapts the gotd parallel downloader onto FetchFunc. The gotd
// downloader always transfers from byte zero, so bytes already present in
// the .part file below Offset are dropped by NewSkipWriterAt: resumed files
// keep their content, while the skipped range is re-transferred (a gotd
// public-API limitation, documented in the design).
func GotdFetch(rpc downloader.Client) FetchFunc {
	return func(ctx context.Context, input Input, dest io.WriterAt) (int64, error) {
		location := input.Location
		if location == nil && input.Refetch != nil {
			fresh, err := input.Refetch(ctx)
			if err != nil {
				return 0, fmt.Errorf("refetch location for %d/%d: %w", input.Item.ChatID, input.Item.MessageID, err)
			}

			location = fresh
		}

		if location == nil {
			return 0, fmt.Errorf("media %s/%d: %w", input.Item.MediaClass, input.Item.MediaID, ErrNoLocation)
		}

		skip := NewSkipWriterAt(dest, input.Offset)

		if _, err := downloader.NewDownloader().Download(rpc, location).Parallel(ctx, skip); err != nil {
			return 0, fmt.Errorf("download %s/%d: %w", input.Item.MediaClass, input.Item.MediaID, err)
		}

		return 0, nil
	}
}

// skipWriterAt hides already-downloaded bytes from the wrapped writer:
// writes fully below offset are dropped and straddling writes are clipped.
type skipWriterAt struct {
	inner  io.WriterAt
	offset int64
}

// NewSkipWriterAt wraps inner so WriteAt calls never persist bytes below
// offset; the reported write count stays truthful for the caller.
func NewSkipWriterAt(inner io.WriterAt, offset int64) io.WriterAt {
	return skipWriterAt{inner: inner, offset: offset}
}

func (w skipWriterAt) WriteAt(chunk []byte, off int64) (int, error) {
	if off >= w.offset {
		written, err := w.inner.WriteAt(chunk, off)
		if err != nil {
			return written, fmt.Errorf("write skipped chunk at %d: %w", off, err)
		}

		return written, nil
	}

	end := off + int64(len(chunk))
	if end <= w.offset {
		return len(chunk), nil
	}

	cut := w.offset - off

	written, err := w.inner.WriteAt(chunk[cut:], w.offset)
	if err != nil {
		return written, fmt.Errorf("write clipped chunk at %d: %w", w.offset, err)
	}

	return int(cut) + written, nil
}

// fileExists reports whether path is present on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
