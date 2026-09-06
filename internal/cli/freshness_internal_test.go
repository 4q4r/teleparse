package cli

import (
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defaultRewalkAge mirrors the [scan] rewalk_min_age default used by the
// freshness tests.
const defaultRewalkAge = 10 * time.Minute

// freshWalkFixture seeds chat 30 at watermark with its last walk stamped
// walkedAgo ago, plus a collector wired like executeRun does.
func freshWalkFixture(t *testing.T, watermark int64, walkedAgo time.Duration) (*store.Store, *walkCollector) {
	t.Helper()

	state := openTestStore(t)
	require.NoError(t, state.UpsertChat(t.Context(), store.Chat{ChatID: 30, Type: "channel"}))

	if watermark > 0 {
		require.NoError(t, state.AdvanceWatermark(t.Context(), 30, watermark, time.Now().Add(-walkedAgo)))
	}

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}
	_, collector := newRunResolver(app, &fakeHistoryAPI{})

	return state, collector
}

// seedManifestRows records done manifest rows for chat 30 so the store has
// a cached match count.
func seedManifestRows(t *testing.T, state *store.Store, ids ...int64) {
	t.Helper()

	for _, id := range ids {
		item := store.MediaItem{
			ChatID: 30, MessageID: id, MediaClass: "photo", MediaID: id,
			Status: store.StatusDone,
		}

		_, err := state.UpsertMedia(t.Context(), &item)
		require.NoError(t, err)
	}
}

func TestWalkTargetSkipsFreshlyWalkedChat(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, 30*time.Second)
	seedManifestRows(t, state, 1, 2, 3)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil, defaultRewalkAge))

	assert.Empty(t, api.requests,
		"a chat walked inside the freshness window must not be probed or paged at all")
	assert.True(t, collector.incremental[30],
		"the skipped chat still counts as incremental so pending manifest rows stay served")
	assert.Equal(t, int64(3), collector.cached[30], "the cached manifest count is stashed for the settled line")
}

func TestWalkTargetSkipSettlesProgressLineWithCachedCount(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, 30*time.Second)
	seedManifestRows(t, state, 1, 2, 3)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	model := scanModel{state: newScanState(timeNow, 1, false), styler: NewStyler(false)}

	// The exact per-chat emission executeRun performs around walkTarget.
	before := len(collector.items)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil, defaultRewalkAge))

	model.Update(scanChatMsg{
		title:   chatLabel(target.Chat.ID, target.Chat.Title),
		matches: len(collector.items) - before,
		cached:  int(collector.cached[target.Chat.ID]),
		took:    time.Millisecond,
	})

	assert.Contains(t, model.View().Content, "+ News (3)",
		"a skipped chat settles instantly with its cached count instead of vanishing")
}

func TestWalkTargetSkipStillServesPendingManifestRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, 30*time.Second)

	pending := store.MediaItem{
		ChatID: 30, MessageID: 7, MediaClass: "photo", MediaID: 7,
		Status: store.StatusDiscovered,
	}

	_, err := state.UpsertMedia(ctx, &pending)
	require.NoError(t, err)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil, defaultRewalkAge))

	require.True(t, collector.incremental[30])

	merged, err := withCachedPending(ctx, state, []scan.Target{target}, collector)
	require.NoError(t, err)

	assert.Len(t, merged, 1, "downloads for a skipped chat come from the pending manifest")
	assert.Equal(t, int64(7), merged[0].MessageID)
}

func TestWalkTargetWalksStaleChat(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, 11*time.Minute)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil, defaultRewalkAge))

	require.NotEmpty(t, api.requests, "a chat older than the freshness window must be walked")
	assert.Equal(t, 500, api.requests[len(api.requests)-1].MinID,
		"a stale chat resumes its incremental walk from the watermark")
}

func TestWalkTargetFullWalkBypassesFreshness(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, time.Second)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true, fullWalk: true}, collector, nil, defaultRewalkAge))

	require.NotEmpty(t, api.requests, "--full must ignore freshness along with the watermark")
	assert.Zero(t, api.requests[len(api.requests)-1].MinID)
	assert.False(t, collector.incremental[30])
}

func TestWalkTargetWalksNeverWalkedChatDespiteFreshness(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 0, 0)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil, defaultRewalkAge))

	require.NotEmpty(t, api.requests, "a chat without a watermark was never walked and must be walked")
	assert.Zero(t, api.requests[len(api.requests)-1].MinID)
}

func TestWalkTargetZeroRewalkAgeAlwaysWalks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, time.Second)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil, 0))

	require.NotEmpty(t, api.requests, "rewalk_min_age = \"0\" must restore walk-always behavior")
}

func TestWalkTargetSyncModeHonorsFreshness(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := freshWalkFixture(t, 500, 30*time.Second)
	seedManifestRows(t, state, 1)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{syncMode: true}, collector, nil, defaultRewalkAge))

	assert.Empty(t, api.requests, "sync runs skip freshly walked chats like every other mode")
	assert.True(t, collector.incremental[30])
	assert.Equal(t, int64(1), collector.cached[30])
}
