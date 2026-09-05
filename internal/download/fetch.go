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
	"github.com/gotd/td/tgerr"
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
		location, err := resolveLocation(ctx, input)
		if err != nil {
			return 0, err
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

// InvokerSource supplies RPC targets bound to a data center. Production is
// tg.DownloadPools: media-only connection pools per DC with a home-DC
// fallback; dc 0 (unknown) maps to the home pool.
type InvokerSource interface {
	InvokerFor(ctx context.Context, dc int) (downloader.Client, error)
}

// downloadRunFunc is the gotd builder call ParallelFetch drives; the seam
// exists so tests can verify pool selection, threads and migration retries
// without a live MTProto connection.
type downloadRunFunc func(
	ctx context.Context,
	rpc downloader.Client,
	location tg.InputFileLocationClass,
	threads int,
	onRetry downloader.RetryHandler,
	dest io.WriterAt,
) error

// ParallelOptions tunes ParallelFetch.
type ParallelOptions struct {
	// Threads is the per-file ranged-part parallelism passed to the gotd
	// downloader builder (1..16, validated by config).
	Threads int
	// Reporter, when it implements ItemReporter, receives flood-wait
	// surfacing from the gotd retry handler. May be nil.
	Reporter Reporter
	// run overrides the gotd builder call; tests only.
	run downloadRunFunc
}

// gotdDownload drives the real gotd parallel downloader: independent ranged
// connections per thread over the supplied RPC target.
func gotdDownload(ctx context.Context, rpc downloader.Client, location tg.InputFileLocationClass,
	threads int, onRetry downloader.RetryHandler, dest io.WriterAt,
) error {
	_, err := downloader.NewDownloader().
		Download(rpc, location).
		WithThreads(threads).
		WithRetryHandler(onRetry).
		Parallel(ctx, dest)
	if err != nil {
		return fmt.Errorf("parallel download: %w", err)
	}

	return nil
}

// ParallelFetch adapts the gotd parallel downloader onto FetchFunc using
// per-DC pooled connections: threads within one file, a connection pool per
// data center across files, and a single retry on FILE_MIGRATE when the
// server moves the file to a DC no pool exists for yet. When pool creation
// fails the transfer falls back to the single primary connection instead of
// failing the run. The gotd downloader always transfers from byte zero, so
// bytes already present in the .part file below Offset are dropped by
// NewSkipWriterAt, exactly as in GotdFetch.
func ParallelFetch(pools InvokerSource, fallback downloader.Client, opts ParallelOptions) FetchFunc {
	run := gotdDownload

	if opts.run != nil {
		run = opts.run
	}

	return func(ctx context.Context, input Input, dest io.WriterAt) (int64, error) {
		location, err := resolveLocation(ctx, input)
		if err != nil {
			return 0, err
		}

		skip := NewSkipWriterAt(dest, input.Offset)

		if err := pooledRun(ctx, pools, fallback, input.DC, location, opts, run, skip); err != nil {
			return 0, fmt.Errorf("download %s/%d: %w", input.Item.MediaClass, input.Item.MediaID, err)
		}

		return 0, nil
	}
}

// pooledRun selects the invoker, runs once and retries once on FILE_MIGRATE
// against the DC named by the server.
func pooledRun(ctx context.Context, pools InvokerSource, fallback downloader.Client, dc int,
	location tg.InputFileLocationClass, opts ParallelOptions, run downloadRunFunc, dest io.WriterAt,
) error {
	rpc := invokerFor(ctx, pools, fallback, dc)

	onRetry := throttleSurfacer(opts.Reporter)

	err := run(ctx, rpc, location, opts.Threads, onRetry, dest)
	if err == nil {
		return nil
	}

	migrated, ok := tgerr.AsType(err, errTypeFileMigrate)
	if !ok || pools == nil {
		return err
	}

	retry := invokerFor(ctx, pools, fallback, migrated.Argument)

	if retryErr := run(ctx, retry, location, opts.Threads, onRetry, dest); retryErr != nil {
		return retryErr
	}

	return nil
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

// throttleSurfacer reports flood waits slept inside gotd retries to the
// reporter's live UI; non-flood retries stay silent.
func throttleSurfacer(reporter Reporter) downloader.RetryHandler {
	item, ok := reporter.(ItemReporter)
	if !ok {
		return func(downloader.RetryEvent) {}
	}

	return func(event downloader.RetryEvent) {
		if wait, flood := tgerr.AsFloodWait(event.Err); flood {
			item.Throttled(int(wait.Seconds()))
		}
	}
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
