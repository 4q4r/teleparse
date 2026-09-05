package download

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// progressRecorder collects the deltas the counting wrapper reports.
type progressRecorder struct {
	deltas []int64
}

func (r *progressRecorder) ItemStart(string, string, int64, int64) {}

func (r *progressRecorder) ItemProgress(_ string, delta int64) {
	r.deltas = append(r.deltas, delta)
}

func (r *progressRecorder) ItemDone(string, bool) {}

func (r *progressRecorder) Throttled(int) {}

// newCountingWriter builds the wrapper over the recorder sink.
func newCountingWriter(sink *progressRecorder) *countingWriterAt {
	return &countingWriterAt{inner: &memWriterAt{}, key: "k", sink: sink}
}

// TestCountingWriterAtHighWaterMarkDeltas pins the Bug B accounting: gotd
// re-requests overlapping ranges on internal retries and ladder attempts,
// so rewritten bytes must contribute zero and only the portion beyond the
// previous maximum written end may count.
func TestCountingWriterAtHighWaterMarkDeltas(t *testing.T) {
	t.Parallel()

	sink := &progressRecorder{}
	writer := newCountingWriter(sink)

	write(t, writer, 100, 0)
	write(t, writer, 100, 0)
	write(t, writer, 130, 50)
	write(t, writer, 10, 90)

	// Zero-delta rewrites are suppressed outright: the item sink only ever
	// sees genuine extensions.
	assert.Equal(t, []int64{100, 80}, sink.deltas,
		"rewrites below the high-water mark must contribute zero")
}

// TestCountingWriterAtFullRewritePassContributesZero models the observed
// 267%/187% live progress: a failed attempt already counted its prefix, and
// every following attempt re-transfers the same range. A full second pass
// over an already-written file must add exactly nothing.
func TestCountingWriterAtFullRewritePassContributesZero(t *testing.T) {
	t.Parallel()

	sink := &progressRecorder{}
	writer := newCountingWriter(sink)

	for range 10 {
		write(t, writer, 100, int64(100)*int64(len(sink.deltas)))
	}

	firstPass := total(sink.deltas)

	for offset := int64(0); offset < 1000; offset += 100 {
		write(t, writer, 100, offset)
	}

	assert.EqualValues(t, 1000, firstPass, "the first pass counts every byte once")
	assert.EqualValues(t, 1000, total(sink.deltas), "the rewrite pass must contribute zero")
}

// TestCountingWriterAtKeepsCountingAcrossLadderRestarts pins that the
// high-water mark survives within one item's retry ladder (the wrapper is
// created once per downloadWithRetries): attempt two resumes at the mark,
// not from zero.
func TestCountingWriterAtKeepsCountingAcrossLadderRestarts(t *testing.T) {
	t.Parallel()

	sink := &progressRecorder{}
	writer := newCountingWriter(sink)

	// Attempt one writes [0,600) then dies.
	write(t, writer, 600, 0)

	// Attempt two re-transfers from zero; the prefix is a rewrite.
	write(t, writer, 300, 0)
	write(t, writer, 300, 300)
	write(t, writer, 400, 600)

	assert.Equal(t, []int64{600, 400}, sink.deltas,
		"the rewritten prefix must stay free and only new bytes count")
}

func write(t *testing.T, writer *countingWriterAt, size int, offset int64) {
	t.Helper()

	chunk := make([]byte, size)

	written, err := writer.WriteAt(chunk, offset)

	require.NoError(t, err)
	assert.Equal(t, size, written)
}

func total(deltas []int64) int64 {
	var sum int64

	for _, delta := range deltas {
		sum += delta
	}

	return sum
}
