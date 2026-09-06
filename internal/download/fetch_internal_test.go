package download

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errNotImplemented and errPoolDownTest back the fetch test doubles.
var (
	errNotImplemented = errors.New("not implemented")
	errPoolDownTest   = errors.New("pool down")
)

// fakeInvoker is a distinguishable downloader.Client stand-in.
type fakeInvoker struct{ dc int }

func (f fakeInvoker) UploadGetFile( //nolint:ireturn // mirrors the gotd seam
	context.Context, *tg.UploadGetFileRequest,
) (tg.UploadFileClass, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadGetFileHashes(
	context.Context, *tg.UploadGetFileHashesRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadReuploadCDNFile(
	context.Context, *tg.UploadReuploadCDNFileRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadGetCDNFileHashes(
	context.Context, *tg.UploadGetCDNFileHashesRequest,
) ([]tg.FileHash, error) {
	return nil, errNotImplemented
}

func (f fakeInvoker) UploadGetWebFile(
	context.Context, *tg.UploadGetWebFileRequest,
) (*tg.UploadWebFile, error) {
	return nil, errNotImplemented
}

// fakePools records InvokerFor traffic and answers from a fixed table.
type fakePools struct {
	byDC  map[int]downloader.Client
	fails map[int]error
	asked []int
}

func (p *fakePools) InvokerFor(_ context.Context, dc int) (downloader.Client, error) { //nolint:ireturn // mirrors the gotd seam
	p.asked = append(p.asked, dc)

	if err, ok := p.fails[dc]; ok {
		return nil, err
	}

	return p.byDC[dc], nil
}

// throttleRecorder captures ItemReporter throttle surfacing.
type throttleRecorder struct {
	NoopReporter
	waits []int
}

func (t *throttleRecorder) Throttled(seconds int)                  { t.waits = append(t.waits, seconds) }
func (t *throttleRecorder) ItemStart(string, string, int64, int64) {}
func (t *throttleRecorder) ItemProgress(string, int64)             {}
func (t *throttleRecorder) ItemDone(string, bool)                  {}

func parallelInput(dc int) Input {
	return Input{
		Item:     store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7},
		Offset:   0,
		Location: &tg.InputDocumentFileLocation{ID: 7},
		DC:       dc,
	}
}

// memWriterAt is an in-memory WriterAt.
type memWriterAt struct{ data []byte }

func (m *memWriterAt) WriteAt(chunk []byte, off int64) (int, error) {
	end := off + int64(len(chunk))

	if int64(len(m.data)) < end {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}

	copy(m.data[off:end], chunk)

	return len(chunk), nil
}

// TestResolveLocationRefetchesWhenMissing pins the hydration contract:
// a nil location with a refetch hook fetches the message once and returns
// its fresh location.
func TestResolveLocationRefetchesWhenMissing(t *testing.T) {
	t.Parallel()

	fresh := &tg.InputDocumentFileLocation{ID: 7}
	refetched := false

	in := Input{Item: store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7}}
	in.Refetch = func(context.Context) (tg.InputFileLocationClass, error) {
		refetched = true

		return fresh, nil
	}

	location, err := resolveLocation(t.Context(), in)
	require.NoError(t, err)
	assert.Same(t, fresh, location)
	assert.True(t, refetched, "the hook must run exactly once")
}

// TestResolveLocationWithoutHooksFails pins the terminal case: no location
// and no hook is the only path that may report ErrNoLocation.
func TestResolveLocationWithoutHooksFails(t *testing.T) {
	t.Parallel()

	in := Input{Item: store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7}}

	_, err := resolveLocation(t.Context(), in)
	require.ErrorIs(t, err, ErrNoLocation)
}

// TestResolveLocationRefetchErrorSurfaces pins that refetch failures keep
// their real cause instead of collapsing into ErrNoLocation.
func TestResolveLocationRefetchErrorSurfaces(t *testing.T) {
	t.Parallel()

	gone := fmt.Errorf("message 2 in chat 1: %w", ErrMessageGone)

	in := Input{Item: store.MediaItem{ChatID: 1, MessageID: 2, MediaIndex: 3, MediaClass: "video", MediaID: 7}}
	in.Refetch = func(context.Context) (tg.InputFileLocationClass, error) {
		return nil, gone
	}

	_, err := resolveLocation(t.Context(), in)
	require.ErrorIs(t, err, ErrMessageGone)
	assert.NotErrorIs(t, err, ErrNoLocation)
}

// TestIsFatalTGErrorClassifiesRefetchOutcomes pins the ladder: refetch
// sentinels are fatal, transport errors stay retryable.
func TestIsFatalTGErrorClassifiesRefetchOutcomes(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err   error
		fatal bool
	}{
		"message gone":       {err: fmt.Errorf("refetch 1/2: %w", ErrMessageGone), fatal: true},
		"media gone":         {err: fmt.Errorf("message 1/2 carries no downloadable media: %w", ErrMediaGone), fatal: true},
		"id invalid on wire": {err: tgerr.New(400, "MESSAGE_ID_INVALID"), fatal: true},
		"network reset": {
			err:   &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")},
			fatal: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.fatal, isFatalTGError(tc.err))
		})
	}
}
