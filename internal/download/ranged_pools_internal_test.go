package download

import (
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/gotd/td/telegram/downloader"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// poolRangedInput builds a DC-routed ranged input.
func poolRangedInput(dc int, offset int64, total int64) Input {
	return Input{
		Item: store.MediaItem{
			ChatID: 1, MessageID: 2, MediaIndex: 3,
			MediaClass: "document", MediaID: 7, Size: int64ptr(total),
		},
		Offset:   offset,
		Location: &tg.InputDocumentFileLocation{ID: 7},
		DC:       dc,
	}
}

// rangedPoolFetch wires a pooled ranged fetch with test-sized knobs.
func rangedPoolFetch(
	pools InvokerSource, fallback downloader.Client, offset, total int64, dc int,
) (FetchFunc, Input) {
	rec := &sleepRecorder{}

	return RangedPoolFetch(pools, fallback, RangedOptions{
		retries: 3,
		backoff: time.Millisecond,
		sleep:   rec.sleep,
	}), poolRangedInput(dc, offset, total)
}

// TestRangedPoolFetchResumesAtOffsetWithoutRedownload is THE production
// regression: a pooled run resuming a 92%-done .part file must receive
// offset == the .part size and request ONLY the remaining tail — no
// chunk ever starts near byte zero like the gotd parallel engine did.
func TestRangedPoolFetchResumesAtOffsetWithoutRedownload(t *testing.T) {
	t.Parallel()

	offset := int64(2*rangedChunkSize + 1234) // the .part bytes on disk
	total := int64(3*rangedChunkSize + 5000)

	data := make([]byte, total)
	for idx := range data {
		data[idx] = byte(idx % 251)
	}

	srv := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	pools := &fakePools{byDC: map[int]downloader.Client{2: srv}, fails: map[int]error{}}

	fetch, in := rangedPoolFetch(pools, fakeInvoker{dc: 9}, offset, total, 2)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), in, dest)
	require.NoError(t, err)

	assert.Equal(t, total-offset, written, "only the missing tail counts")

	require.NotEmpty(t, srv.calls)

	for _, call := range srv.calls {
		assert.GreaterOrEqual(t, call.offset, offset-rangedChunkAlign,
			"every request must start at the resume point (within alignment), never byte zero")
		assert.Less(t, call.offset, total, "every request must target the remaining tail")
	}

	assert.Equal(t, alignDown(offset, rangedChunkAlign), srv.calls[0].offset,
		"the first request floors the resume offset onto the 4096 boundary")

	assert.Equal(t, make([]byte, offset), dest.data[:offset],
		"bytes below the resume offset must stay untouched")
	assert.Equal(t, data[offset:], dest.data[offset:])

	assert.Equal(t, []int{2}, pools.asked, "the item's DC must resolve its pool")
}

// TestRangedPoolFetchFileMigrateReResolvesPool pins the seam adaptation:
// a FILE_MIGRATE answer under the ranged engine re-resolves the invoker
// for the server-named DC and re-requests the SAME chunk there — the
// whole-item restart the parallel engine needed is gone.
func TestRangedPoolFetchFileMigrateReResolvesPool(t *testing.T) {
	t.Parallel()

	total := int64(rangedChunkSize + 700)

	data := make([]byte, total)

	dc2 := &rangedServer{
		data:    data,
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(303, "FILE_MIGRATE_4")},
	}

	dc4 := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	pools := &fakePools{byDC: map[int]downloader.Client{2: dc2, 4: dc4}, fails: map[int]error{}}

	fetch, in := rangedPoolFetch(pools, fakeInvoker{dc: 9}, 0, total, 2)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), in, dest)
	require.NoError(t, err)

	assert.Equal(t, total, written)
	assert.Equal(t, data, dest.data)

	assert.Equal(t, []int{2, 4}, pools.asked, "the migration must re-resolve the DC 4 pool")
	require.Len(t, dc2.calls, 1, "the migrated-from pool serves exactly the rejected chunk")
	require.GreaterOrEqual(t, len(dc4.calls), 1)
	assert.EqualValues(t, 0, dc4.calls[0].offset, "the re-request targets the same chunk on the new DC")
}

// TestRangedPoolFetchPoolFailureFallsBack pins the degradation contract:
// a pool that cannot be created rides the fallback primary connection
// instead of failing the transfer.
func TestRangedPoolFetchPoolFailureFallsBack(t *testing.T) {
	t.Parallel()

	total := int64(rangedChunkSize + 100)

	data := make([]byte, total)

	primary := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	pools := &fakePools{byDC: map[int]downloader.Client{}, fails: map[int]error{2: errPoolDownTest}}

	fetch, in := rangedPoolFetch(pools, primary, 0, total, 2)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), in, dest)
	require.NoError(t, err)

	assert.Equal(t, total, written)
	assert.Equal(t, data, dest.data)
	assert.Equal(t, []int{2}, pools.asked)
}

// TestRangedPoolFetchUnknownDCUsesHomePool pins DC 0 (unknown) semantics:
// the home pool answers, exactly as the pooled parallel engine routed it.
func TestRangedPoolFetchUnknownDCUsesHomePool(t *testing.T) {
	t.Parallel()

	total := int64(100)

	data := make([]byte, total)

	home := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	pools := &fakePools{byDC: map[int]downloader.Client{0: home}, fails: map[int]error{}}

	fetch, in := rangedPoolFetch(pools, fakeInvoker{dc: 9}, 0, total, 0)

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), in, dest)
	require.NoError(t, err)

	assert.Equal(t, total, written)
	assert.Equal(t, []int{0}, pools.asked, "DC 0 must resolve the home pool")
}

// TestFetchForRidesRangedWithPools pins the wire: pooled runs ride the
// ranged engine too — a fatal rpc answer climbs as a ranged error, never
// as a parallel-engine error.
func TestFetchForRidesRangedWithPools(t *testing.T) {
	t.Parallel()

	srv := &rangedServer{
		data:    []byte("0123456789"),
		failN:   map[int64]int{0: 1},
		failErr: map[int64]error{0: tgerr.New(403, "TAKEOUT_FILE_TOO_BIG")},
	}

	pools := &fakePools{byDC: map[int]downloader.Client{2: srv}, fails: map[int]error{}}

	fetch := FetchFor(pools, fakeInvoker{dc: 9}, FetchOptions{})

	_, err := fetch(t.Context(), poolRangedInput(2, 0, 10), &memWriterAt{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ranged download", "pooled runs must ride the ranged engine")
}

// TestRangedPoolFetchNilPoolsRidesFallback pins the takeout shape through
// the pooled constructor: nil pools degrade to the fixed fallback client.
func TestRangedPoolFetchNilPoolsRidesFallback(t *testing.T) {
	t.Parallel()

	total := int64(100)

	data := make([]byte, total)

	fallback := &rangedServer{data: data, failN: map[int64]int{}, failErr: map[int64]error{}}

	fetch := RangedPoolFetch(nil, fallback, RangedOptions{retries: 3, backoff: time.Millisecond})

	dest := &memWriterAt{}

	written, err := fetch(t.Context(), poolRangedInput(2, 0, total), dest)
	require.NoError(t, err)

	assert.Equal(t, total, written)
}
