package download_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/pace"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedResolver maps every item onto <root>/<name> with a fixed sidecar.
type fixedResolver struct {
	root string
	name string
	meta *download.SidecarMeta
}

func (r fixedResolver) Resolve(item store.MediaItem) (download.Resolved, error) {
	return download.Resolved{
		Path: filepath.Join(r.root, r.name),
		Meta: r.meta,
	}, nil
}

// byteSource returns a FetchFunc writing content starting at Input.Offset,
// simulating a ranged byte source.
func byteSource(content string) download.FetchFunc {
	return func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		offset := int(in.Offset)
		if offset > len(content) {
			offset = len(content)
		}

		written, err := dest.WriteAt([]byte(content[offset:]), in.Offset)

		return int64(written), err
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = st.Close() })

	return st
}

func managerConfig(mods ...func(*download.Config)) download.Config {
	cfg := download.Config{
		Concurrency: 2,
		RetryMax:    3,
		ClaimBatch:  10,
		Dedupe:      "unique-id",
		BackoffBase: time.Millisecond,
		HookTimeout: 2 * time.Second,
		Output: config.Output{
			Collision:  "index",
			PartSuffix: ".part",
		},
	}
	for _, mod := range mods {
		mod(&cfg)
	}

	return cfg
}

func newPacer(threshold time.Duration) *pace.Pacer {
	return pace.New(pace.Config{
		Concurrency:         2,
		DelayMin:            0,
		DelayMax:            0,
		FloodSleepThreshold: threshold,
	})
}

// newManagerIn returns a manager writing into a fresh temp dir and reports
// that dir so tests can pre-seed and inspect files.
func newManagerIn(t *testing.T, state *store.Store, cfg download.Config,
	fetch download.FetchFunc, meta *download.SidecarMeta,
) (string, *download.Manager) {
	t.Helper()

	root := t.TempDir()
	mgr := download.NewManager(state, newPacer(time.Minute), cfg,
		fixedResolver{root: root, name: "file.bin", meta: meta}, download.NoopReporter{})
	mgr.Fetch = fetch

	return root, mgr
}

func queuedItem(chatID, msgID int64, size int64) store.MediaItem {
	return store.MediaItem{
		ChatID:     chatID,
		MessageID:  msgID,
		MediaID:    msgID,
		MediaClass: "document",
		Size:       &size,
		Status:     store.StatusQueued,
	}
}

func enqueue(t *testing.T, mgr *download.Manager, items ...store.MediaItem) {
	t.Helper()

	require.NoError(t, mgr.Enqueue(context.Background(), items))
}

func TestManagerDownloadsItems(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root, mgr := newManagerIn(t, st, managerConfig(), byteSource("hello world"), nil)
	enqueue(t, mgr, queuedItem(1, 10, 11))

	res, err := mgr.Run(context.Background(), "run-basic")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 11, res.Bytes)
	assert.NoFileExists(t, filepath.Join(root, "file.bin.part"), "part file renamed away")

	row, exists, err := st.MediaByFile(context.Background(), "document", 10)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusDone, row.Status)
	assert.NotNil(t, row.Path)
}

func TestManagerResumeFromPartFile(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	var offsetSeen int64

	fetch := func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		offsetSeen = in.Offset

		written, err := dest.WriteAt([]byte(" world"), in.Offset)

		return int64(written), err
	}

	root, mgr := newManagerIn(t, st, managerConfig(), fetch, nil)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.bin.part"), []byte("hello"), 0o600))
	enqueue(t, mgr, queuedItem(1, 5, 11))

	res, err := mgr.Run(context.Background(), "run-resume")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 5, offsetSeen, "resume must continue at the part file size")

	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(got))
}

func TestManagerCollisionIndex(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root, mgr := newManagerIn(t, st, managerConfig(), byteSource("data"), nil)
	enqueue(t, mgr, queuedItem(1, 1, 4), queuedItem(2, 2, 4))

	res, err := mgr.Run(context.Background(), "run-index")

	require.NoError(t, err)
	assert.EqualValues(t, 2, res.Downloaded)

	assert.FileExists(t, filepath.Join(root, "file.bin"))
	assert.FileExists(t, filepath.Join(root, "file (1).bin"))
}

func TestManagerCollisionSkip(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	var fetches atomic.Int64

	fetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		fetches.Add(1)

		return 0, nil
	}

	cfg := managerConfig(func(c *download.Config) { c.Output.Collision = "skip" })

	root, mgr := newManagerIn(t, st, cfg, fetch, nil)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.bin"), []byte("old"), 0o600))
	enqueue(t, mgr, queuedItem(1, 1, 3))

	res, err := mgr.Run(context.Background(), "run-skip")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Skipped)
	assert.EqualValues(t, 0, res.Downloaded)
	assert.EqualValues(t, 0, fetches.Load())

	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(got))
}

func TestManagerCollisionOverwrite(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	cfg := managerConfig(func(c *download.Config) { c.Output.Collision = "overwrite" })

	root, mgr := newManagerIn(t, st, cfg, byteSource("new!"), nil)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.bin"), []byte("old"), 0o600))
	enqueue(t, mgr, queuedItem(1, 1, 4))

	res, err := mgr.Run(context.Background(), "run-overwrite")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)

	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)
	assert.Equal(t, "new!", string(got))
}

func TestManagerSha256(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	cfg := managerConfig(func(c *download.Config) { c.Output.Sha256 = true })

	_, mgr := newManagerIn(t, st, cfg, byteSource("payload"), nil)
	enqueue(t, mgr, queuedItem(1, 7, 7))

	_, err := mgr.Run(context.Background(), "run-sha")
	require.NoError(t, err)

	sum := sha256.Sum256([]byte("payload"))

	row, exists, err := st.MediaByFile(context.Background(), "document", 7)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotNil(t, row.Sha256)
	assert.Equal(t, hex.EncodeToString(sum[:]), *row.Sha256)
}

func TestManagerSidecarWritten(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	root, mgr := newManagerIn(t, st, managerConfig(func(c *download.Config) { c.Output.Sidecar = true }),
		byteSource("x"), &download.SidecarMeta{MessageID: 42, ChatTitle: "News", Text: "hello"})
	enqueue(t, mgr, queuedItem(1, 42, 1))

	_, err := mgr.Run(context.Background(), "run-sidecar")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(root, "file.bin.json"))
	require.NoError(t, err)

	var meta download.SidecarMeta

	require.NoError(t, json.Unmarshal(raw, &meta)) //nolint:musttag // mirrors the sidecar marshal side
	assert.Equal(t, int64(42), meta.MessageID)
	assert.Equal(t, "News", meta.ChatTitle)
}

func TestManagerHooksExecute(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	marker := filepath.Join(t.TempDir(), "marker")
	cfg := managerConfig(func(c *download.Config) {
		c.Hooks.PostDownload = []string{"touch " + marker, "false"}
	})

	_, mgr := newManagerIn(t, st, cfg, byteSource("x"), nil)
	enqueue(t, mgr, queuedItem(1, 3, 1))

	res, err := mgr.Run(context.Background(), "run-hooks")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded, "failing hook must stay non-fatal")
	assert.FileExists(t, marker)
}

func TestManagerFloodWaitParksRun(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	require.NoError(t, st.CreateRun(context.Background(), &store.Run{RunID: "run-flood"}))

	flood := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		return 0, tgerr.New(420, "FLOOD_WAIT_30")
	}

	root := t.TempDir()
	mgr := download.NewManager(
		st, newPacer(5*time.Second), managerConfig(),
		fixedResolver{root: root, name: "file.bin"},
		download.NoopReporter{},
	)
	mgr.Fetch = flood

	enqueue(t, mgr, queuedItem(1, 10, 5))

	res, err := mgr.Run(context.Background(), "run-flood")

	require.NoError(t, err)
	assert.True(t, res.Parked)
	assert.WithinDuration(t, time.Now().Add(30*time.Second), res.ResumeAt, 2*time.Second)

	run, exists, err := st.GetRun(context.Background(), "run-flood")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, store.StatusParked, run.Status)
}

func TestManagerRetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	var calls atomic.Int64

	fetch := func(ctx context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		if calls.Add(1) < 3 {
			return 0, errors.New("transient network hiccup")
		}

		return byteSource("ok")(ctx, in, dest)
	}

	_, mgr := newManagerIn(t, st, managerConfig(), fetch, nil)
	enqueue(t, mgr, queuedItem(1, 9, 2))

	res, err := mgr.Run(context.Background(), "run-retry")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Downloaded)
	assert.EqualValues(t, 3, calls.Load())

	row, _, err := st.MediaByFile(context.Background(), "document", 9)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDone, row.Status)
	assert.Equal(t, 2, row.Attempts, "two failures recorded before success")
}

func TestManagerFatalErrorFailsFast(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	var calls atomic.Int64

	fetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		calls.Add(1)

		return 0, tgerr.New(400, "MESSAGE_ID_INVALID")
	}

	_, mgr := newManagerIn(t, st, managerConfig(), fetch, nil)
	enqueue(t, mgr, queuedItem(1, 8, 5))

	res, err := mgr.Run(context.Background(), "run-fatal")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Failed)
	assert.EqualValues(t, 1, calls.Load(), "fatal errors must not retry")

	row, _, err := st.MediaByFile(context.Background(), "document", 8)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, row.Status)
}

func TestManagerDedupeSkipsKnownFile(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	done := queuedItem(1, 100, 3)
	done.Status = store.StatusDone
	require.NoError(t, st.UpsertMedia(context.Background(), &done))

	var fetches atomic.Int64

	fetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		fetches.Add(1)

		return 0, nil
	}

	_, mgr := newManagerIn(t, st, managerConfig(), fetch, nil)

	// Same unique file (document/100) attached to a different message.
	duplicate := queuedItem(2, 200, 3)
	duplicate.MediaID = 100
	enqueue(t, mgr, duplicate)

	res, err := mgr.Run(context.Background(), "run-dedupe")

	require.NoError(t, err)
	assert.EqualValues(t, 0, res.Downloaded)
	assert.EqualValues(t, 0, fetches.Load())
}

func TestManagerSkipExistingOption(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	cfg := managerConfig(func(c *download.Config) { c.SkipExisting = true })

	var fetches atomic.Int64

	fetch := func(context.Context, download.Input, io.WriterAt) (int64, error) {
		fetches.Add(1)

		return 0, nil
	}

	root, mgr := newManagerIn(t, st, cfg, fetch, nil)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.bin"), []byte("here"), 0o600))
	enqueue(t, mgr, queuedItem(1, 6, 4))

	res, err := mgr.Run(context.Background(), "run-skipexisting")

	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Skipped)
	assert.EqualValues(t, 0, fetches.Load())

	row, _, err := st.MediaByFile(context.Background(), "document", 6)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDone, row.Status)
}

func TestManagerFailsWithoutFetch(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	_, mgr := newManagerIn(t, st, managerConfig(), nil, nil)
	mgr.Fetch = nil

	_, err := mgr.Run(context.Background(), "run-nofetch")
	require.ErrorIs(t, err, download.ErrNoFetch)
}

func TestManagerFileSizeMismatchRetries(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	_, mgr := newManagerIn(t, st, managerConfig(), byteSource("short"), nil)
	enqueue(t, mgr, queuedItem(1, 4, 100))

	res, err := mgr.Run(context.Background(), "run-mismatch")

	require.NoError(t, err)
	assert.EqualValues(t, 0, res.Downloaded)
	assert.EqualValues(t, 1, res.Failed)

	row, _, err := st.MediaByFile(context.Background(), "document", 4)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, row.Status)
}

func TestSkipWriterAtDropsBelowOffset(t *testing.T) {
	t.Parallel()

	var buf []byte

	dest := writerAtFunc(func(p []byte, off int64) (int, error) {
		end := off + int64(len(p))
		if int(end) > len(buf) {
			grown := make([]byte, end)
			copy(grown, buf)
			buf = grown
		}

		copy(buf[off:end], p)

		return len(p), nil
	})

	skipped := download.NewSkipWriterAt(dest, 5)

	written, err := skipped.WriteAt([]byte("abcde"), 0)
	require.NoError(t, err)
	assert.Equal(t, 5, written)
	assert.Empty(t, buf)

	written, err = skipped.WriteAt([]byte("fghij"), 5)
	require.NoError(t, err)
	assert.Equal(t, 5, written)

	written, err = skipped.WriteAt([]byte("KLMNO"), 3)
	require.NoError(t, err)
	assert.Equal(t, 5, written)
	assert.Equal(t, "MNOij", string(buf[5:]), "straddling write lands its tail at the offset")
}

type writerAtFunc func(p []byte, off int64) (int, error)

func (f writerAtFunc) WriteAt(p []byte, off int64) (int, error) { return f(p, off) }
