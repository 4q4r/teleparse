package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/4q4r/teleparse/internal/transport"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Ranged-engine tuning mirrors the gotd downloader contract (512KiB
// parts at 4096-aligned offsets, precise requests): a bounded number of
// idempotent re-requests per chunk with a small exponential backoff,
// and the flood-wait ceiling slept inside the fetch — longer waits climb
// so the run pacer can park.
const (
	rangedChunkSize     = 512 * 1024
	rangedChunkAlign    = 4096
	rangedChunkRetries  = 12
	rangedBackoffBase   = time.Second
	rangedBackoffMax    = 9 * time.Second
	rangedBackoffGrowth = 3
	rangedFloodSleepMax = 15 * time.Second
	rangedServerErrCode = 500
)

// ErrCDNRedirectUnsupported reports an upload.fileCdnRedirect answer to
// a ranged request: the CDN flow needs its own connection pool, which
// the single takeout session does not have.
var ErrCDNRedirectUnsupported = errors.New("cdn redirect unsupported in ranged mode")

// ErrUnexpectedChunkResult reports upload.getFile answers that are
// neither a file chunk nor a redirect.
var ErrUnexpectedChunkResult = errors.New("unexpected upload.getFile result")

// ErrShortFile reports an upload.getFile answer that ends before the
// manifest's total: a content verdict that fails permanently. Deliberately
// not EOF-shaped so the dead-transport predicate cannot turn it into a
// resumable carrier death.
var ErrShortFile = errors.New("server ended the file before the manifest total")

// RangedOptions tunes RangedFetch.
type RangedOptions struct {
	// Reporter, when it implements ItemReporter, receives flood-wait
	// surfacing. May be nil.
	Reporter Reporter
	// pools resolves per-DC RPC targets for pooled runs; nil rides the
	// fixed fallback client (single-connection takeout sessions).
	pools InvokerSource
	// fallback is the RPC target used when pools is nil and the one pool
	// resolution degrades to on failure.
	fallback downloader.Client
	// retries bounds transient re-requests per chunk; tests only.
	retries int
	// backoff is the base delay between chunk retries; tests only.
	backoff time.Duration
	// sleep parks for flood waits and retry backoff; tests only.
	sleep func(context.Context, time.Duration) error
}

// rangedConfig is RangedOptions with defaults applied.
type rangedConfig struct {
	items   ItemReporter
	retries int
	backoff time.Duration
	sleep   func(context.Context, time.Duration) error
}

// resolve fills the test-only gaps with the production defaults.
func (o RangedOptions) resolve() rangedConfig {
	cfg := rangedConfig{
		items:   asItemReporter(o.Reporter),
		retries: rangedChunkRetries,
		backoff: rangedBackoffBase,
		sleep:   sleepContext,
	}

	if o.retries > 0 {
		cfg.retries = o.retries
	}

	if o.backoff > 0 {
		cfg.backoff = o.backoff
	}

	if o.sleep != nil {
		cfg.sleep = o.sleep
	}

	return cfg
}

// rangedState carries one ranged transfer: the current rpc target, the
// pool seam (nil for fixed-target runs), the input (refetch seam), the
// current location, the DC the file last lived on, the destination and
// the total byte count (0 when unknown).
type rangedState struct {
	rpc      downloader.Client
	pools    InvokerSource
	fallback downloader.Client
	dc       int
	cfg      rangedConfig
	input    Input
	location tg.InputFileLocationClass
	dest     io.WriterAt
	total    int64
}

// RangedFetch adapts explicit upload.getFile ranged requests onto
// FetchFunc for a single fixed RPC target — the takeout session's one
// invoker, where every download multiplexes onto one connection. Each
// 512KiB chunk is requested at its absolute offset, so a dropped
// connection costs only the CURRENT chunk: transient failures re-request
// the same idempotent range over the redialed connection instead of
// restarting the file at byte zero like the gotd downloader. The redial
// is implicit and fresh: gotd's reconnection loop (telegram/connect.go)
// replaces the primary connection behind an exponential 100ms..5s backoff
// once the carrier dies, so the next chunk request dials a new proxied
// tunnel — no forced-reconnect hook is needed.
//
// Transfers resume at Input.Offset — nothing below the offset is ever
// requested beyond the final partial 4KiB block (re-fetched and re-written
// byte-identically to sit on the server's offset alignment). The Manager
// retry ladder above this fetch is the whole-item reconnect budget: it
// re-enters at the current .part size, so exhausting the per-chunk budget
// costs one attempt, never the bytes.
func RangedFetch(rpc downloader.Client, opts RangedOptions) FetchFunc {
	opts.fallback = rpc
	opts.pools = nil

	return rangedFetch(opts)
}

// RangedPoolFetch drives the same ranged engine over per-DC pooled
// connections: every item resolves its RPC target through the
// InvokerSource seam (home-DC pool for dc 0, the fallback primary
// connection when pool creation fails), and a FILE_MIGRATE answer
// re-resolves the pool for the server-named DC and re-requests the SAME
// idempotent chunk — no whole-item restart, no byte-zero re-transfer.
// Chunks stay sequential per file, yet pooled: gotd's pool.Invoke hands
// each request an idle pooled connection and releases it afterwards, so
// the manager's concurrent items interleave across the pool. Nil pools
// degrade to the fixed fallback client (the takeout shape).
func RangedPoolFetch(pools InvokerSource, fallback downloader.Client, opts RangedOptions) FetchFunc {
	if pools == nil {
		return RangedFetch(fallback, opts)
	}

	opts.pools = pools
	opts.fallback = fallback

	return rangedFetch(opts)
}

// rangedFetch builds the universal engine over resolved options.
func rangedFetch(opts RangedOptions) FetchFunc {
	cfg := opts.resolve()

	return func(ctx context.Context, input Input, dest io.WriterAt) (int64, error) {
		location, err := resolveLocation(ctx, input)
		if err != nil {
			return 0, err
		}

		state := &rangedState{
			rpc:      invokerFor(ctx, opts.pools, opts.fallback, input.DC),
			pools:    opts.pools,
			fallback: opts.fallback,
			dc:       input.DC,
			cfg:      cfg,
			input:    input,
			location: location,
			dest:     dest,
			total:    itemSizeOf(input.Item),
		}

		pos := input.Offset

		for {
			next, done, err := state.next(ctx, pos)
			if err != nil {
				return next - input.Offset, fmt.Errorf("ranged download %s/%d: %w",
					input.Item.MediaClass, input.Item.MediaID, err)
			}

			if done {
				return next - input.Offset, nil
			}

			pos = next
		}
	}
}

// next transfers one aligned request starting at pos and reports the
// advanced position plus whether the transfer is complete. The request
// floors pos onto the 4096 boundary, so a mid-chunk resume re-fetches at
// most the final partial block; the already-present prefix of the
// answer is dropped.
func (state *rangedState) next(ctx context.Context, pos int64) (int64, bool, error) {
	reqOff := alignDown(pos, rangedChunkAlign)

	data, err := state.request(ctx, reqOff)
	if err != nil {
		return pos, false, err
	}

	skip := pos - reqOff

	if int64(len(data)) <= skip {
		// The server answered nothing past the resume point: end of
		// file, or a short file when the manifest promised more.
		if state.total > 0 && pos < state.total {
			return pos, false, fmt.Errorf("short file: %d of %d bytes at %d: %w",
				pos, state.total, reqOff, ErrShortFile)
		}

		return pos, true, nil
	}

	data = data[skip:]

	if state.total > 0 && pos+int64(len(data)) > state.total {
		data = data[:state.total-pos]
	}

	if _, err := state.dest.WriteAt(data, pos); err != nil {
		return pos, false, fmt.Errorf("write %d bytes at %d: %w", len(data), pos, err)
	}

	pos += int64(len(data))

	if state.total > 0 {
		return pos, pos >= state.total, nil
	}

	// Unknown total: gotd's end-of-file heuristic — an empty or short
	// answer means the server has nothing past this range.
	return pos, int64(len(data))+skip < rangedChunkSize, nil
}

// request fetches the 512KiB chunk at reqOff with bounded idempotent
// re-requests. Short flood waits and expired file references never burn
// the chunk budget (the wait or the refetch RPC is the throttle); a
// dead context, a long flood wait, a fatal rpc error or budget
// exhaustion climbs immediately so the Manager ladder sees it.
func (state *rangedState) request(ctx context.Context, reqOff int64) ([]byte, error) {
	retries := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("chunk at %d: %w", reqOff, err)
		}

		data, err := state.chunkOnce(ctx, reqOff)
		if err == nil {
			return data, nil
		}

		if wait, flood := tgerr.AsFloodWait(err); flood {
			if state.cfg.items != nil {
				state.cfg.items.Throttled(int(wait.Seconds()))
			}

			if wait > rangedFloodSleepMax {
				return nil, fmt.Errorf("chunk at %d: %w", reqOff, err)
			}

			if sleepErr := state.cfg.sleep(ctx, wait); sleepErr != nil {
				return nil, fmt.Errorf("chunk at %d: %w", reqOff, sleepErr)
			}

			continue
		}

		if tgerr.Is(err, tg.ErrFileReferenceExpired) && state.input.Refetch != nil {
			fresh, refetchErr := state.input.Refetch(ctx)
			if refetchErr != nil {
				return nil, fmt.Errorf("refetch after expired reference at %d: %w", reqOff, refetchErr)
			}

			state.location = fresh

			continue
		}

		if migrated, ok := tgerr.AsType(err, errTypeFileMigrate); ok && state.pools != nil {
			// The file lives on another DC: re-resolve the pool for the
			// server-named DC and re-request the same idempotent chunk —
			// migration costs no budget and no bytes. Pooled runs only;
			// a single-connection session cannot follow (climbs as-is).
			state.dc = migrated.Argument
			state.rpc = invokerFor(ctx, state.pools, state.fallback, state.dc)

			continue
		}

		if !rangedTransient(ctx, err) {
			return nil, fmt.Errorf("chunk at %d: %w", reqOff, err)
		}

		if retries >= state.cfg.retries {
			return nil, fmt.Errorf("chunk at %d after %d retries: %w", reqOff, retries, err)
		}

		retries++

		if sleepErr := state.cfg.sleep(ctx, rangedBackoff(retries, state.cfg.backoff)); sleepErr != nil {
			return nil, fmt.Errorf("chunk at %d: %w", reqOff, sleepErr)
		}
	}
}

// chunkOnce fires one upload.getFile and unpacks its answer.
func (state *rangedState) chunkOnce(ctx context.Context, reqOff int64) ([]byte, error) {
	result, err := state.rpc.UploadGetFile(ctx, &tg.UploadGetFileRequest{
		Location: state.location,
		Offset:   reqOff,
		Limit:    rangedChunkSize,
		Precise:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("rpc get file at %d: %w", reqOff, err)
	}

	return chunkBytes(result)
}

// chunkBytes unpacks an upload.getFile answer into the chunk payload;
// redirects and unknown answers climb as errors.
func chunkBytes(result tg.UploadFileClass) ([]byte, error) {
	switch file := result.(type) {
	case *tg.UploadFile:
		return file.Bytes, nil
	case *tg.UploadFileCDNRedirect:
		return nil, fmt.Errorf("cdn dc %d: %w", file.DCID, ErrCDNRedirectUnsupported)
	default:
		return nil, fmt.Errorf("type %T: %w", result, ErrUnexpectedChunkResult)
	}
}

// rangedTransient reports chunk errors worth an idempotent re-request:
// a wrapped cancellation while the run lives is gotd's engine
// force-closing on a dropped connection (a dead run context is the user
// interrupt and climbs immediately), dead carriers — a local proxy
// killing the tunnel mid-write (EPIPE), resets, EOF-shaped write deaths,
// including type-less chains matched by message — are redials, other
// network errors are redials too, and 5xx rpc answers are server-side
// hiccups.
func rangedTransient(ctx context.Context, err error) bool {
	if isCancellationErr(err) {
		return ctx.Err() == nil
	}

	if transport.IsDeadTransport(err) {
		return true
	}

	var netErr net.Error

	if errors.As(err, &netErr) {
		return true
	}

	var rpcErr *tgerr.Error

	return errors.As(err, &rpcErr) && rpcErr.Code >= rangedServerErrCode
}

// rangedBackoff grows the chunk retry delay — base, 3×, 9× — and caps
// it there.
func rangedBackoff(retry int, base time.Duration) time.Duration {
	delay := base

	for range retry - 1 {
		delay *= rangedBackoffGrowth

		if delay >= rangedBackoffMax {
			return rangedBackoffMax
		}
	}

	return delay
}

// alignDown floors offset onto the upload.getFile granularity.
func alignDown(offset, align int64) int64 {
	return offset &^ (align - 1)
}

// sleepContext parks for delay or until ctx dies.
func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("sleep: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// FetchOptions tunes the ranged engine FetchFor wires.
type FetchOptions struct {
	// Reporter receives flood-wait surfacing; may be nil.
	Reporter Reporter
}

// FetchFor wires the transfer engine for a run — the ranged sequential
// engine everywhere. The gotd parallel downloader this replaces ALWAYS
// re-transfers from byte zero, so a resumed .part file wrote everything
// below its offset into the void: zero counted progress at 92-98% with
// 0B/s while gigabytes silently re-downloaded. The ranged engine resumes
// at the exact on-disk offset on every path. Nil pools (an active takeout
// forbids raw media connections) rides the single fallback invoker;
// pooled runs resolve per-DC targets through the InvokerSource seam with
// home-DC fallback and FILE_MIGRATE re-resolution.
func FetchFor(pools InvokerSource, fallback downloader.Client, opts FetchOptions) FetchFunc {
	return RangedPoolFetch(pools, fallback, RangedOptions{Reporter: opts.Reporter})
}
