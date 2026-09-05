package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/pace"
	"github.com/4q4r/teleparse/internal/store"

	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Tuning defaults applied by NewManager when Config leaves them unset.
const (
	defaultClaimBatch   = 32
	defaultBackoff      = 500 * time.Millisecond
	defaultHookTimeout  = 10 * time.Second
	backoffMax          = 30 * time.Second
	collisionIndexMax   = 1000
	filePermDownload    = 0o600
	dirPermDownload     = 0o700
	reportEveryAttempts = 1
)

// Config carries the output, pacing, hook and dedupe knobs the Manager
// needs; the CLI projects config.Config plus the effective filter options
// onto it.
type Config struct {
	Output       config.Output
	Hooks        config.Hooks
	Concurrency  int
	RetryMax     int
	ClaimBatch   int
	Dedupe       string // hardlink | unique-id | hash | off; hash implies the unique-id pre-check
	SkipExisting bool
	BackoffBase  time.Duration
	HookTimeout  time.Duration
	// Root is the resolved downloads root; hardlink mode keeps its blob
	// store under Root/.teleparse/blobs so links never cross filesystems.
	Root string
}

// Resolved is the resolver output for one media item: the final path, the
// optional sidecar metadata, the current file location, the data center the
// file is stored on (0 when unknown) and a refetch hook for expired file
// references.
type Resolved struct {
	Path     string
	Meta     *SidecarMeta
	Location tg.InputFileLocationClass
	DC       int
	Refetch  RefetchFunc
}

// ItemResolver renders the final path, sidecar metadata and file location
// for a media item. Production resolves from the walked message context;
// resume without context falls back to store fields.
type ItemResolver interface {
	Resolve(item store.MediaItem) (Resolved, error)
}

// Result summarizes one Manager run.
type Result struct {
	Downloaded int64
	Skipped    int64
	Failed     int64
	Retries    int64
	Bytes      int64
	Duplicates int64
	// Linked counts duplicate occurrences served by a hardlink (or its
	// copy fallback) with zero network traffic.
	Linked int64
	// LinkCopies counts how many of those links fell back to byte copies
	// on filesystems without hardlink support.
	LinkCopies   int64
	Parked       bool
	ResumeAt     time.Time
	FailedByChat map[int64]int64
}

// Manager coordinates the download pipeline: enqueue with dedupe, paced
// claiming per chat, retrying ranged downloads with .part resume, collision
// handling, sidecars, hooks and flood-wait parking.
type Manager struct {
	store    *store.Store
	pacer    *pace.Pacer
	cfg      Config
	resolve  ItemResolver
	reporter Reporter
	items    ItemReporter
	Fetch    FetchFunc
	// Link creates hardlinks for the blob store; overridable for tests.
	Link             LinkFunc
	now              func() time.Time
	linkFallbackOnce sync.Once

	chats        map[int64]struct{}
	duplicates   int64
	pendingLinks []store.MediaItem
}

// NewManager returns a Manager over the given state store, pacer, config,
// item resolver and progress reporter; defaults clamp unset Config fields.
func NewManager(
	state *store.Store,
	pacer *pace.Pacer,
	cfg Config,
	resolve ItemResolver,
	reporter Reporter,
) *Manager {
	if cfg.ClaimBatch < 1 {
		cfg.ClaimBatch = defaultClaimBatch
	}

	if cfg.RetryMax < 1 {
		cfg.RetryMax = 1
	}

	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}

	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = defaultBackoff
	}

	if cfg.HookTimeout <= 0 {
		cfg.HookTimeout = defaultHookTimeout
	}

	if cfg.Output.PartSuffix == "" {
		cfg.Output.PartSuffix = ".part"
	}

	if reporter == nil {
		reporter = NoopReporter{}
	}

	return &Manager{
		store:    state,
		pacer:    pacer,
		cfg:      cfg,
		resolve:  resolve,
		reporter: reporter,
		items:    asItemReporter(reporter),
		Link:     os.Link,
		now:      time.Now,
		chats:    map[int64]struct{}{},
	}
}

// asItemReporter probes the reporter for live-UI capability; plain
// Reporter implementations opt out by not implementing ItemReporter.
//
//nolint:ireturn // the capability probe is the point
func asItemReporter(reporter Reporter) ItemReporter {
	item, ok := reporter.(ItemReporter)
	if !ok {
		return nil
	}

	return item
}

// Enqueue upserts discovered media as queued and remembers the chats they
// belong to. With Dedupe unique-id or hash, items whose (class, media_id)
// is already done are skipped entirely; "hash" additionally treats a
// post-download sha256 equal to an existing done row as a duplicate (the
// pre-check itself is identical to unique-id). "off" re-downloads.
// "hardlink" instead records every duplicate sighting — done-known or
// unique-file-conflicted — as a pending occurrence, and Run later links
// each occurrence's chat to the shared blob so every chat gets its file.
// A file already tracked under a different message — including the same
// file forwarded into another chat — is a benign skip counted into
// Result.Duplicates; Enqueue never aborts on it, and the chat of the
// duplicate sighting is never registered, so it stays download-free.
func (m *Manager) Enqueue(ctx context.Context, items []store.MediaItem) error {
	for idx := range items {
		item := items[idx]
		item.Status = store.StatusQueued
		item.Attempts = 0

		if dedupeEnabled(m.cfg.Dedupe) {
			known, exists, err := m.store.MediaByFile(ctx, item.MediaClass, item.MediaID)
			if err != nil {
				return fmt.Errorf("dedupe lookup %s/%d: %w", item.MediaClass, item.MediaID, err)
			}

			if exists && known.Status == store.StatusDone {
				m.duplicates++
				m.rememberOccurrence(item)

				continue
			}
		}

		stored, err := m.store.UpsertMedia(ctx, &item)
		if err != nil {
			return fmt.Errorf("enqueue %d/%d/%d: %w", item.ChatID, item.MessageID, item.MediaIndex, err)
		}

		if !stored {
			m.duplicates++
			m.rememberOccurrence(item)

			continue
		}

		m.chats[item.ChatID] = struct{}{}
	}

	return nil
}

// runState is the shared, mutex-guarded outcome of a run plus its cancel
// function used to stop workers and the claim feeder.
type runState struct {
	mu     sync.Mutex
	res    Result
	cancel context.CancelFunc
}

func (s *runState) mutate(fn func(res *Result)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn(&s.res)
}

func (s *runState) result() Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.res
}

// Run drains every enqueued chat: workers claim pending rows, pace, fetch,
// retry and promote them. A flood wait beyond the pacer threshold parks the
// run (resume_at recorded, run finished as parked) and returns early.
func (m *Manager) Run(ctx context.Context, runID string) (Result, error) {
	if m.Fetch == nil {
		return Result{}, fmt.Errorf("run %s: %w", runID, ErrNoFetch)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := &runState{res: Result{
		FailedByChat: map[int64]int64{},
		// Enqueue ran before Run; carry its duplicate count into the
		// reported outcome.
		Duplicates: m.duplicates,
	}, cancel: cancel}
	items := make(chan store.MediaItem)

	var workers sync.WaitGroup

	for range m.cfg.Concurrency {
		workers.Add(1)

		go m.worker(runCtx, ctx, runID, items, &workers, state)
	}

	errs := make(chan error, 1)

	go m.feed(runCtx, items, errs)

	workers.Wait()

	// Hardlink dedupe: duplicate sightings recorded by Enqueue get their
	// links now that this run's blobs exist. runCtx governs the drain so a
	// flood-wait park inside it stops further occurrences.
	m.drainPendingLinks(runCtx, ctx, runID, state)

	var feedErr error

	select {
	case feedErr = <-errs:
	default: // feeder always sends; defensive default keeps Run total.
	}

	return state.result(), feedErr
}

// feed claims pending rows chat by chat and hands them to the workers,
// sending exactly one error (or nil) before returning.
func (m *Manager) feed(ctx context.Context, items chan<- store.MediaItem, errs chan<- error) {
	defer func() { errs <- nil }()

	defer close(items)

	for _, chatID := range m.sortedChats() {
		for {
			if ctx.Err() != nil {
				return
			}

			batch, err := m.store.ClaimPending(ctx, chatID, m.cfg.ClaimBatch, m.cfg.RetryMax)
			if err != nil {
				errs <- fmt.Errorf("claim pending chat %d: %w", chatID, err)

				return
			}

			if len(batch) == 0 {
				break
			}

			for _, item := range batch {
				select {
				case <-ctx.Done():
					return
				case items <- *item:
				}
			}
		}
	}
}

func (m *Manager) sortedChats() []int64 {
	ids := make([]int64, 0, len(m.chats))
	for chatID := range m.chats {
		ids = append(ids, chatID)
	}

	for idx := 1; idx < len(ids); idx++ {
		for jdx := idx; jdx > 0 && ids[jdx] < ids[jdx-1]; jdx-- {
			ids[jdx], ids[jdx-1] = ids[jdx-1], ids[jdx]
		}
	}

	return ids
}

func (m *Manager) worker(runCtx, bookCtx context.Context, runID string,
	items <-chan store.MediaItem, wg *sync.WaitGroup, state *runState,
) {
	defer wg.Done()

	for item := range items {
		if runCtx.Err() != nil {
			continue
		}

		m.processItem(runCtx, bookCtx, runID, item, state)
	}
}

func (m *Manager) processItem(runCtx, bookCtx context.Context, runID string,
	item store.MediaItem, state *runState,
) {
	resolved, err := m.resolve.Resolve(item)
	if err != nil {
		m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)

		return
	}

	if m.cfg.SkipExisting && fileExists(resolved.Path) {
		m.skipItem(bookCtx, state, item, resolved.Path)

		return
	}

	if m.hardlinkMode() {
		m.processHardlinkItem(runCtx, bookCtx, runID, item, resolved, state)

		return
	}

	finalPath, skip, err := m.finalPathFor(resolved.Path)
	if err != nil {
		m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)

		return
	}

	if skip {
		m.skipItem(bookCtx, state, item, finalPath)

		return
	}

	m.downloadWithRetries(runCtx, bookCtx, runID, item, resolved, finalPath, state)
}

func (m *Manager) downloadWithRetries(runCtx, bookCtx context.Context, runID string,
	item store.MediaItem, resolved Resolved, finalPath string, state *runState,
) {
	// Hardlink mode downloads into the blob store (.part lives next to the
	// blob) and promotes there; verifyAndPromote then links the final path.
	promotePath := finalPath

	if m.hardlinkMode() {
		promotePath = blobPathOf(m.cfg.Root, item)
	}

	partPath := promotePath + m.cfg.Output.PartSuffix

	part, offset, err := openPartFile(partPath, item.BytesDone)
	if err != nil {
		m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)

		return
	}

	defer func() { _ = part.Close() }()

	location := resolved.Location

	key := itemKeyOf(item)

	m.itemStart(key, itemLabel(item, finalPath), itemSizeOf(item), offset)

	dest := m.countingAt(key, part)

	attempt := item.Attempts

	var lastErr error

	for attempt < m.cfg.RetryMax {
		attempt++

		err := m.attemptOnce(runCtx, item, dest, offset, location, resolved)
		if err == nil {
			promoteErr := m.verifyAndPromote(bookCtx, state, item, resolved, part, promotePath, finalPath)

			if promoteErr == nil {
				m.itemDone(key, false)

				return
			} else {
				err = promoteErr
			}
		}

		lastErr = err

		if m.handleFailure(runCtx, bookCtx, runID, state, item, err, attempt, resolved, &location) {
			return
		}

		m.recordAttempt(bookCtx, item, err, attempt)

		// Count only retries that actually follow: the last attempt of an
		// exhausted ladder never gets one.
		if attempt < m.cfg.RetryMax {
			state.mutate(func(res *Result) {
				res.Retries++
			})

			m.reporter.Inc("retries", reportEveryAttempts)
		}

		if err := m.sleepBackoff(runCtx, attempt); err != nil {
			m.failItem(bookCtx, state, item, err, attempt)

			return
		}

		if err := m.pacer.Wait(runCtx); err != nil {
			m.failItem(bookCtx, state, item, err, attempt)

			return
		}
	}

	// The ladder is exhausted: only now does the row leave downloading, so
	// the terminal failure is visible to later runs and crash recovery.
	m.recordFailure(bookCtx, item, lastErr, attempt)

	m.itemFailed(key, attempt, lastErr)

	state.mutate(func(res *Result) {
		res.Failed++
		res.FailedByChat[item.ChatID]++
	})

	m.reporter.Inc("failed", reportEveryAttempts)
}

// attemptOnce paces, fetches one ranged transfer and reports the raw error
// for classification.
func (m *Manager) attemptOnce(ctx context.Context, item store.MediaItem,
	dest io.WriterAt, offset int64, location tg.InputFileLocationClass, resolved Resolved,
) error {
	if err := m.pacer.Acquire(ctx); err != nil {
		return fmt.Errorf("acquire pace slot for %d/%d: %w", item.ChatID, item.MessageID, err)
	}

	defer m.pacer.Release()

	if err := m.pacer.Wait(ctx); err != nil {
		return fmt.Errorf("pace before %d/%d: %w", item.ChatID, item.MessageID, err)
	}

	in := Input{Item: item, Offset: offset, Location: location, DC: resolved.DC}

	if _, err := m.Fetch(ctx, in, dest); err != nil {
		return fmt.Errorf("fetch %d/%d/%d: %w", item.ChatID, item.MessageID, item.MediaIndex, err)
	}

	return nil
}

// handleFailure classifies a failed attempt and reports whether item
// processing must stop (parked, fatal or cancelled). Retryable errors
// return false so the attempt loop backs off and retries.
func (m *Manager) handleFailure(runCtx, bookCtx context.Context, runID string, state *runState,
	item store.MediaItem, err error, attempt int, resolved Resolved, location *tg.InputFileLocationClass,
) bool {
	switch {
	case runCtx.Err() != nil:
		m.failItem(bookCtx, state, item, err, attempt)

		return true
	case isFatalTGError(err):
		m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)

		return true
	default:
		return m.handleFloodOrRefetch(runCtx, bookCtx, runID, state, item, err, attempt, resolved, location)
	}
}

func (m *Manager) handleFloodOrRefetch(runCtx, bookCtx context.Context, runID string, state *runState,
	item store.MediaItem, err error, attempt int, resolved Resolved, location *tg.InputFileLocationClass,
) bool {
	// FLOOD_WAIT_%d (code 420, core.telegram.org/api/errors): d is the
	// server-mandated pause in seconds — account-bound, never IP-bound.
	if wait, ok := tgerr.AsFloodWait(err); ok {
		seconds := int(wait.Seconds())
		m.pacer.ReportFlood(seconds)

		if m.pacer.ShouldPark(seconds) {
			m.parkRun(bookCtx, runID, state, item, err, attempt, seconds)

			return true
		}

		return false
	}

	// FILE_REFERENCE_EXPIRED: file references expire server-side; refetch the
	// source message (messages.getMessages / channels.getMessages) to mint a
	// fresh reference and retry the same transfer.
	if tgerr.Is(err, tg.ErrFileReferenceExpired) && resolved.Refetch != nil {
		if fresh, refetchErr := resolved.Refetch(runCtx); refetchErr == nil {
			*location = fresh
		}
	}

	return false
}

func (m *Manager) parkRun(bookCtx context.Context, runID string, state *runState,
	item store.MediaItem, err error, attempt, seconds int,
) {
	resumeAt := m.now().Add(time.Duration(seconds) * time.Second)

	m.itemDone(itemKeyOf(item), true)

	m.recordAttempt(bookCtx, item, err, attempt)

	if err := m.store.SetResumeAt(bookCtx, runID, resumeAt); err != nil {
		m.reporter.Inc("store_errors", 1)
	}

	if err := m.store.FinishRun(bookCtx, runID, store.StatusParked, ""); err != nil {
		m.reporter.Inc("store_errors", 1)
	}

	state.mutate(func(res *Result) {
		res.Parked = true
		res.ResumeAt = resumeAt
	})

	state.cancel()
}

// verifyAndPromote fsyncs the part file, verifies its size, optionally
// hashes it, renames it onto the promote path (the final path, or the blob
// in hardlink mode — which then hardlinks onto the final path), writes the
// sidecar, records done and runs the post-download hooks.
func (m *Manager) verifyAndPromote(bookCtx context.Context, state *runState,
	item store.MediaItem, resolved Resolved, part *os.File, promotePath, finalPath string,
) error {
	if err := part.Sync(); err != nil {
		return fmt.Errorf("sync part file: %w", err)
	}

	info, err := part.Stat()
	if err != nil {
		return fmt.Errorf("stat part file: %w", err)
	}

	if item.Size != nil && info.Size() != *item.Size {
		return fmt.Errorf("size mismatch for %d/%d: got %d want %d: %w",
			item.ChatID, item.MessageID, info.Size(), *item.Size, ErrSizeMismatch)
	}

	var shaHex *string

	if m.cfg.Output.Sha256 {
		summed, err := hashFile(part)
		if err != nil {
			return fmt.Errorf("hash %d/%d: %w", item.ChatID, item.MessageID, err)
		}

		shaHex = &summed
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), dirPermDownload); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if err := os.Rename(part.Name(), promotePath); err != nil {
		return fmt.Errorf("promote part file: %w", err)
	}

	if promotePath != finalPath {
		// Hardlink mode: the blob now holds the canonical bytes; serve the
		// final path from it. Link problems degrade to a copy, never
		// failing an item whose bytes exist.
		if err := m.linkOrCopy(state, promotePath, finalPath); err != nil {
			return fmt.Errorf("link blob to final path: %w", err)
		}
	}

	if m.cfg.Output.Sidecar && resolved.Meta != nil {
		if err := writeSidecar(finalPath+".json", resolved.Meta); err != nil {
			m.reporter.Inc("sidecar_errors", 1)
		}
	}

	if err := m.store.MarkDone(bookCtx, item.ChatID, item.MessageID, item.MediaIndex, finalPath, shaHex); err != nil {
		m.reporter.Inc("store_errors", 1)
	}

	m.runHooks(bookCtx, finalPath)

	size := info.Size()

	state.mutate(func(res *Result) {
		res.Downloaded++
		res.Bytes += size
	})

	m.reporter.Inc("downloaded", reportEveryAttempts)
	m.reporter.Inc("bytes", size)

	return nil
}

// finalPathFor applies the collision policy when the final path already
// exists: index appends " (n)" before the extension, overwrite keeps the
// path and skip asks the caller to treat the item as already present.
func (m *Manager) finalPathFor(path string) (string, bool, error) {
	if !fileExists(path) {
		return path, false, nil
	}

	switch m.cfg.Output.Collision {
	case "overwrite":
		return path, false, nil
	case "skip":
		return path, true, nil
	default:
		indexed, err := indexFreePath(path)
		if err != nil {
			return path, false, fmt.Errorf("index collision for %s: %w", path, err)
		}

		return indexed, false, nil
	}
}

// ErrNoFreeIndex reports an exhausted collision-index namespace.
var ErrNoFreeIndex = errors.New("no free collision index")

func indexFreePath(path string) (string, error) {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)

	for idx := 1; idx <= collisionIndexMax; idx++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, idx, ext)
		if !fileExists(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("below %d: %w", collisionIndexMax, ErrNoFreeIndex)
}

// openPartFile opens (creating if needed) the .part file and reports the
// resume offset: the current file size, since on-disk bytes are the only
// truth after a crash (stored bytes_done may lag behind). The parent
// directory is created first: templated final paths commonly nest several
// levels ({chat}/{date}/...) that no earlier stage materializes.
func openPartFile(partPath string, _ int64) (*os.File, int64, error) {
	if err := os.MkdirAll(filepath.Dir(partPath), dirPermDownload); err != nil {
		return nil, 0, fmt.Errorf("create part dir for %s: %w", partPath, err)
	}

	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, filePermDownload)
	if err != nil {
		return nil, 0, fmt.Errorf("open part file %s: %w", partPath, err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()

		return nil, 0, fmt.Errorf("stat part file %s: %w", partPath, err)
	}

	return file, info.Size(), nil
}

// ErrSizeMismatch reports a completed transfer whose byte count disagrees
// with the manifest.
var ErrSizeMismatch = errors.New("downloaded size mismatch")

func hashFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek for hashing: %w", err)
	}

	sum := sha256.New()

	if _, err := io.Copy(sum, file); err != nil {
		return "", fmt.Errorf("read for hashing: %w", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind after hashing: %w", err)
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}

func writeSidecar(path string, meta *SidecarMeta) error {
	encoded, err := json.MarshalIndent(meta, "", "  ") //nolint:musttag // nested filters types are out of edit scope
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}

	if err := os.WriteFile(path, append(encoded, '\n'), filePermDownload); err != nil {
		return fmt.Errorf("write sidecar %s: %w", path, err)
	}

	return nil
}

// runHooks executes each post-download hook template with {path}
// substituted; hook failures are logged, never fatal.
func (m *Manager) runHooks(ctx context.Context, path string) {
	for _, template := range m.cfg.Hooks.PostDownload {
		command := strings.ReplaceAll(template, "{path}", path)
		words := strings.Fields(command)

		if len(words) == 0 {
			continue
		}

		hookCtx, cancel := context.WithTimeout(ctx, m.cfg.HookTimeout)

		cmd := exec.CommandContext(hookCtx, words[0], words[1:]...) //nolint:gosec // hooks are user config by design

		if err := cmd.Run(); err != nil {
			m.reporter.Inc("hook_errors", 1)
		}

		cancel()
	}
}

// recordAttempt bumps the attempt counter of an in-flight item without
// releasing its claim: the row stays downloading, so the feeder cannot
// re-claim it while the retry ladder is still running.
func (m *Manager) recordAttempt(ctx context.Context, item store.MediaItem, err error, attempts int) {
	if err := m.store.RecordAttempt(ctx, item.ChatID, item.MessageID, item.MediaIndex, err.Error(), attempts); err != nil {
		m.reporter.Inc("store_errors", 1)
	}
}

// recordFailure stores one failed attempt without counting the item failed.
func (m *Manager) recordFailure(ctx context.Context, item store.MediaItem, err error, attempts int) {
	if err := m.store.MarkFailed(ctx, item.ChatID, item.MessageID, item.MediaIndex, err.Error(), attempts); err != nil {
		m.reporter.Inc("store_errors", 1)
	}
}

// failItem records the attempt and counts the item failed overall.
func (m *Manager) failItem(ctx context.Context, state *runState,
	item store.MediaItem, err error, attempts int,
) {
	m.recordFailure(ctx, item, err, attempts)

	m.itemFailed(itemKeyOf(item), attempts, err)

	state.mutate(func(res *Result) {
		res.Failed++
		res.FailedByChat[item.ChatID]++
	})

	m.reporter.Inc("failed", reportEveryAttempts)
}

// skipItem marks an item done at an already-present path.
func (m *Manager) skipItem(ctx context.Context, state *runState, item store.MediaItem, path string) {
	if err := m.store.MarkDone(ctx, item.ChatID, item.MessageID, item.MediaIndex, path, nil); err != nil {
		m.reporter.Inc("store_errors", 1)
	}

	state.mutate(func(res *Result) {
		res.Skipped++
	})

	m.reporter.Inc("skipped", reportEveryAttempts)
}

func (m *Manager) sleepBackoff(ctx context.Context, attempt int) error {
	delay := m.cfg.BackoffBase
	for range attempt - 1 {
		delay *= 2
		if delay >= backoffMax {
			delay = backoffMax

			break
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("backoff: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// isFatalTGError reports Telegram errors that no retry can fix: the message
// or its media is gone for good.
func isFatalTGError(err error) bool {
	switch {
	case tgerr.Is(err, "MESSAGE_ID_INVALID"),
		tgerr.Is(err, "MESSAGE_DELETED"),
		tgerr.Is(err, "MEDIA_EMPTY"),
		tgerr.Is(err, "MEDIA_INVALID"):
		return true
	default:
		return false
	}
}

// itemKeyOf builds the stable reporter identity of a media row.
func itemKeyOf(item store.MediaItem) string {
	return fmt.Sprintf("%d/%d/%d", item.ChatID, item.MessageID, item.MediaIndex)
}

// itemLabel picks the live-UI display name: the manifest filename when
// known, else the final path's base name.
func itemLabel(item store.MediaItem, finalPath string) string {
	if item.Filename != nil && *item.Filename != "" {
		return *item.Filename
	}

	if base := filepath.Base(finalPath); base != "" && base != "." {
		return base
	}

	return fmt.Sprintf("%d_%d_%d", item.ChatID, item.MessageID, item.MediaIndex)
}

// itemSizeOf dereferences the manifest size, 0 meaning unknown.
func itemSizeOf(item store.MediaItem) int64 {
	if item.Size == nil {
		return 0
	}

	return *item.Size
}

// itemStart forwards a transfer registration when the reporter opted in.
func (m *Manager) itemStart(key, name string, total, offset int64) {
	if m.items != nil {
		m.items.ItemStart(key, name, total, offset)
	}
}

// itemDone forwards a transfer retirement when the reporter opted in;
// unknown keys are ignored by reporters, so unconditional emission is safe.
func (m *Manager) itemDone(key string, failed bool) {
	if m.items != nil {
		m.items.ItemDone(key, failed)
	}
}

// itemFailed retires a failed transfer: reporters that implement
// FailureDetailRenderer take over rendering the failure line (and the
// plain ItemDone call is skipped for them); others get ItemDone(key, true).
func (m *Manager) itemFailed(key string, attempts int, err error) {
	if detail, ok := m.reporter.(FailureDetailReporter); ok {
		detail.ItemFailedDetail(key, attempts, err)

		return
	}

	m.itemDone(key, true)
}

// countingAt wraps dest with byte accounting when the reporter opted in;
// plain reporters keep the unwrapped destination.
func (m *Manager) countingAt(key string, dest io.WriterAt) io.WriterAt {
	if m.items == nil {
		return dest
	}

	return countingWriterAt{inner: dest, key: key, sink: m.items}
}

// countingWriterAt reports every persisted byte delta to the item sink.
type countingWriterAt struct {
	inner io.WriterAt
	key   string
	sink  ItemReporter
}

func (w countingWriterAt) WriteAt(chunk []byte, off int64) (int, error) {
	written, err := w.inner.WriteAt(chunk, off)
	if written > 0 {
		w.sink.ItemProgress(w.key, int64(written))
	}

	if err != nil {
		return written, fmt.Errorf("counted write at %d: %w", off, err)
	}

	return written, nil
}

func dedupeEnabled(mode string) bool {
	return mode == "hardlink" || mode == "unique-id" || mode == "hash"
}
