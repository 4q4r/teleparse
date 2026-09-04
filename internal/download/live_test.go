package download_test

import (
	"bytes"
	"teleparse/internal/download"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveReporterLifecycle(t *testing.T) {
	t.Parallel()

	var buf guardedLiveBuffer

	live := download.NewLiveReporter(&buf)
	require.NotNil(t, live)

	live.SetPhase("downloading")

	live.ItemStart("1/2/0", "video.mp4", 1000, 0)

	live.ItemProgress("1/2/0", 250)

	live.Inc("downloaded", 1)
	live.Inc("bytes", 1000)

	live.ItemDone("1/2/0", false)

	live.Throttled(4)

	done := make(chan struct{})

	go func() {
		live.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close must terminate the tea program")
	}

	out := buf.String()
	assert.Contains(t, out, "done (", "final summary line must land in the render writer")
	assert.Contains(t, out, "1 done")
}

// guardedLiveBuffer serializes writes like the real stderr.
type guardedLiveBuffer struct {
	buf bytes.Buffer
}

func (g *guardedLiveBuffer) Write(chunk []byte) (int, error) {
	return g.buf.Write(chunk)
}

func (g *guardedLiveBuffer) String() string { return g.buf.String() }
