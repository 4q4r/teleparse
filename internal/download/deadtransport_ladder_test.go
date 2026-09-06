package download_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadProxyExhaustion reproduces the exact production FAIL shape from
// the 12h50m takeout run behind a local HTTP CONNECT proxy
// (127.0.0.1:10809): the proxy killed long-lived connections, every
// chunk re-request died with the same broken pipe, the chunk budget and
// then the ladder burned — and the string-only chain (type lost crossing
// gotd wraps) must still classify as a dead carrier at exhaustion.
func deadProxyExhaustion() error {
	return fmt.Errorf(
		"ranged download video/24: chunk at %d after 12 retries: rpc get file at %d: "+
			"send: write: write intermediate: write tcp 127.0.0.1:38246->127.0.0.1:10809: write: broken pipe",
		512*1024, 512*1024)
}

// deadProxyTypedChain is the typed twin of deadProxyExhaustion: the same
// send/write/write-intermediate wraps over a *net.OpError carrying
// syscall.EPIPE.
func deadProxyTypedChain() error {
	return fmt.Errorf("send: %w", fmt.Errorf("write: %w", fmt.Errorf("write intermediate: %w", &net.OpError{
		Op:     "write",
		Net:    "tcp",
		Source: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 38246},
		Addr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10809},
		Err:    os.NewSyscallError("write", syscall.EPIPE),
	})))
}

// TestRunDeadTransportExhaustionSettlesInterrupted pins the backstop for
// a proxy staying dead for minutes: repeated dead-carrier failures stay
// retryable inside the ladder (re-entry at the .part offset), and the
// exhausted ladder settles INTERRUPTED-resumable — never terminally
// failed — exactly like the cancellation flavors, so a later run with a
// healthy proxy completes the file.
func TestRunDeadTransportExhaustionSettlesInterrupted(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &interruptRecorder{}

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)

	mgr.Fetch = func(context.Context, download.Input, io.WriterAt) (int64, error) {
		return 0, deadProxyExhaustion()
	}

	enqueue(t, mgr, queuedItem(1, 10, 11))

	res, err := mgr.Run(context.Background(), "run-deadproxy")
	require.NoError(t, err)

	assert.EqualValues(t, 0, res.Failed, "a dead local proxy is not the file's failure")
	assert.EqualValues(t, 1, res.Interrupted, "the exhausted ladder must settle interrupted")
	assert.EqualValues(t, 2, res.Retries, "retry_max 3 means two retries before settling")
	assert.Empty(t, res.FailedByChat)

	require.Len(t, recorder.interrupted, 1)
	assert.Empty(t, recorder.failures, "no FAIL reason may render for a resumable interrupt")

	assert.FileExists(t, filepath.Join(root, "file.bin.part"), "the .part bytes must stay for resume")
}

// TestRunDeadTransportTypedChainExhaustionSettlesInterrupted is the
// typed-chain twin: ECONNRESET from the carrier classifies identically.
func TestRunDeadTransportTypedChainExhaustionSettlesInterrupted(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	recorder := &interruptRecorder{}

	root := t.TempDir()

	mgr := download.NewManager(state, newPacer(time.Minute), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, recorder)

	mgr.Fetch = func(_ context.Context, _ download.Input, _ io.WriterAt) (int64, error) {
		return 0, fmt.Errorf("fetch 1/2/3: %w", deadProxyTypedChain())
	}

	enqueue(t, mgr, queuedItem(1, 10, 11))

	res, err := mgr.Run(context.Background(), "run-deadproxy-typed")
	require.NoError(t, err)

	assert.EqualValues(t, 0, res.Failed)
	assert.EqualValues(t, 1, res.Interrupted)

	require.Len(t, recorder.interrupted, 1)
	assert.Empty(t, recorder.failures)
}

// TestRunDeadTransportRetriesReEnterAtPartOffset pins the re-entry
// contract for dead carriers: a proxy death mid-file burns one ladder
// attempt, the re-entry receives the CURRENT on-disk offset, and a
// recovered proxy completes the file — byte zero is requested once.
func TestRunDeadTransportRetriesReEnterAtPartOffset(t *testing.T) {
	t.Parallel()

	state := newTestStore(t)

	root := t.TempDir()

	recorder := &offsetRecorder{}

	mgr := download.NewManager(state, newPacer(0), managerConfig(),
		fixedResolver{root: root, name: "file.bin"}, &detailRecorder{})

	mgr.Fetch = func(_ context.Context, in download.Input, dest io.WriterAt) (int64, error) {
		recorder.record(in.Offset)

		if in.Offset == 0 {
			_, err := dest.WriteAt([]byte("AAAAA"), 0)
			require.NoError(t, err)

			// The proxy dies mid-file: half the item persisted, the
			// in-flight write hit the dead socket.
			return 0, deadProxyTypedChain()
		}

		written, err := dest.WriteAt([]byte("BBBBB"), in.Offset)

		return int64(written), err
	}

	enqueue(t, mgr, queuedItem(1, 10, 10))

	res, err := mgr.Run(context.Background(), "run-deadproxy-offset")
	require.NoError(t, err)

	assert.EqualValues(t, 1, res.Downloaded, "the file must complete once the proxy recovers")
	assert.EqualValues(t, 0, res.Failed)

	assert.Equal(t, []int64{0, 5}, recorder.snapshot(),
		"the ladder re-entry must receive the current on-disk offset")

	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	require.NoError(t, err)
	assert.Equal(t, "AAAAABBBBB", string(got), "the persisted prefix must survive untouched")
}
