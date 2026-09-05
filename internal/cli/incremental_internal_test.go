package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()

	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = state.Close() })

	return state
}

func timeNow() time.Time { return time.Now() }

// fakeHistoryAPI canned-answers the history and sender surfaces the walk
// needs, recording every getHistory request for assertion.
type fakeHistoryAPI struct {
	requests []*tg.MessagesGetHistoryRequest
	history  tg.MessagesMessagesClass
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeHistoryAPI) MessagesGetHistory(
	ctx context.Context, request *tg.MessagesGetHistoryRequest,
) (tg.MessagesMessagesClass, error) {
	f.requests = append(f.requests, request)

	if f.history != nil {
		return f.history, nil
	}

	return &tg.MessagesMessages{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeHistoryAPI) MessagesSearch(
	ctx context.Context, request *tg.MessagesSearchRequest,
) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeHistoryAPI) MessagesGetReplies(
	ctx context.Context, request *tg.MessagesGetRepliesRequest,
) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{}, nil
}

func (f *fakeHistoryAPI) MessagesGetForumTopics(
	ctx context.Context, request *tg.MessagesGetForumTopicsRequest,
) (*tg.MessagesForumTopics, error) {
	return &tg.MessagesForumTopics{}, nil
}

func (f *fakeHistoryAPI) UsersGetUsers(
	ctx context.Context, id []tg.InputUserClass,
) ([]tg.UserClass, error) {
	return nil, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeHistoryAPI) ChannelsGetChannels(
	ctx context.Context, id []tg.InputChannelClass,
) (tg.MessagesChatsClass, error) {
	return &tg.MessagesChats{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeHistoryAPI) MessagesGetMessages(
	ctx context.Context, id []tg.InputMessageClass,
) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{}, nil
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *fakeHistoryAPI) ChannelsGetMessages(
	ctx context.Context, request *tg.ChannelsGetMessagesRequest,
) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{}, nil
}

// newWalkFixture opens a temp store with chat 30 seeded at watermark and a
// collector wired like executeRun does.
func newWalkFixture(t *testing.T, watermark int64) (*store.Store, *walkCollector) {
	t.Helper()

	state := openTestStore(t)
	require.NoError(t, state.UpsertChat(t.Context(), store.Chat{ChatID: 30, Type: "channel"}))

	if watermark > 0 {
		require.NoError(t, state.AdvanceWatermark(t.Context(), 30, watermark, timeNow()))
	}

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}
	_, collector := newRunResolver(app, &fakeHistoryAPI{})

	return state, collector
}

func walkTestTarget(chatID int64, newest int64) scan.Target {
	msg := &tg.Message{ID: int(newest), Date: 1700000000}
	msg.PeerID = &tg.PeerChannel{ChannelID: chatID}

	return scan.Target{
		InputPeer: &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 1},
		Chat:      filters.Chat{ID: chatID, Type: "channel", Title: "News"},
		NewestID:  newest,
	}
}

// historyPageWith builds a result holding one plain message of the given id.
func historyPageWith(id int) *tg.MessagesMessages {
	msg := &tg.Message{ID: id, Date: 1700000000, Message: "x"}
	msg.PeerID = &tg.PeerChannel{ChannelID: 30}

	return &tg.MessagesMessages{Messages: []tg.MessageClass{msg}}
}

func TestWalkTargetInjectsWatermarkAsMinID(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := newWalkFixture(t, 500)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil))

	require.NotEmpty(t, api.requests)
	assert.Equal(t, 500, api.requests[len(api.requests)-1].MinID,
		"incremental walk must push the watermark down as min_id")
	assert.True(t, collector.incremental[30], "chat marked as walked incrementally")
}

func TestWalkTargetProbesNewestWhenScopeHadNoDialogData(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := newWalkFixture(t, 500)

	api := &fakeHistoryAPI{history: historyPageWith(600)}
	target := walkTestTarget(30, 0) // explicit spec: newest id unknown

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil))

	require.GreaterOrEqual(t, len(api.requests), 2, "probe plus walk pages")

	probe := api.requests[0]
	assert.Equal(t, 1, probe.Limit, "the newest-id probe fetches a single message")
	assert.Equal(t, int64(500), int64(api.requests[1].MinID), "walk starts at the watermark")
	assert.True(t, collector.incremental[30])
}

func TestWalkTargetResetsClearedHistoryAndRewalks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := newWalkFixture(t, 5000)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 42) // newest far below the watermark

	buf := &bytes.Buffer{}
	progress := &scanProgress{out: buf, styler: NewStyler(false), total: 1}

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, progress))

	wm, err := state.Watermark(ctx, 30)
	require.NoError(t, err)
	assert.Zero(t, wm, "cleared chat must reset its watermark")

	require.NotEmpty(t, api.requests)
	assert.Zero(t, api.requests[len(api.requests)-1].MinID, "cleared chat re-walks in full")
	assert.Contains(t, buf.String(), "history cleared: News (re-walking)")
	assert.False(t, collector.incremental[30], "cleared chat is not an incremental walk")
}

func TestWalkTargetSyncModeWalksFromWatermarkRegardlessOfIncrementalFlag(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := newWalkFixture(t, 300)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{syncMode: true}, collector, nil))

	require.NotEmpty(t, api.requests)
	assert.Equal(t, 300, api.requests[len(api.requests)-1].MinID,
		"sync keeps its watermark semantics untouched")
}

func TestWalkTargetFullWalkFlagForcesCompleteRewalk(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := newWalkFixture(t, 300)

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true, fullWalk: true}, collector, nil))

	require.NotEmpty(t, api.requests)
	assert.Zero(t, api.requests[len(api.requests)-1].MinID, "--full must bypass the watermark")
	assert.False(t, collector.incremental[30])
}

func TestWalkTargetStashesCachedMatchedCount(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state, collector := newWalkFixture(t, 500)

	for id := int64(1); id <= 3; id++ {
		item := store.MediaItem{
			ChatID: 30, MessageID: id, MediaClass: "photo", MediaID: id,
			Status: store.StatusDone,
		}
		require.NoError(t, state.UpsertMedia(ctx, &item))
	}

	api := &fakeHistoryAPI{}
	target := walkTestTarget(30, 900)

	require.NoError(t, walkTarget(ctx, state, api, target,
		&filters.Plan{}, filters.Options{}, runMode{incremental: true}, collector, nil))

	assert.Equal(t, int64(3), collector.cached[30], "prior-run manifest rows are stashed for count totals")
}

func TestAdvanceWalkedWatermarksMovesAllSeenChats(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state := openTestStore(t)

	for _, chatID := range []int64{30, 31} {
		require.NoError(t, state.UpsertChat(ctx, store.Chat{ChatID: chatID, Type: "channel"}))
	}

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}

	_, collector := newRunResolver(app, &fakeHistoryAPI{})
	collector.maxSeen[30] = 700
	collector.maxSeen[31] = 900

	require.NoError(t, advanceWalkedWatermarks(ctx, state, collector, []scan.Target{
		walkTestTarget(30, 0), walkTestTarget(31, 0),
	}))

	wm, err := state.Watermark(ctx, 30)
	require.NoError(t, err)
	assert.Equal(t, int64(700), wm, "preview walks must build the cache from run one")

	wm, err = state.Watermark(ctx, 31)
	require.NoError(t, err)
	assert.Equal(t, int64(900), wm)
}

func TestWithCachedPendingMergesManifestRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	state := openTestStore(t)
	require.NoError(t, state.UpsertChat(ctx, store.Chat{ChatID: 30, Type: "channel"}))

	rows := []store.MediaItem{
		{ChatID: 30, MessageID: 5, MediaClass: "photo", MediaID: 5, Status: store.StatusDiscovered},
		{ChatID: 30, MessageID: 9, MediaClass: "photo", MediaID: 9, Status: store.StatusQueued},
		{ChatID: 30, MessageID: 12, MediaClass: "photo", MediaID: 12, Status: store.StatusDone},
	}

	for idx := range rows {
		require.NoError(t, state.UpsertMedia(ctx, &rows[idx]))
	}

	app := &App{cfg: config.Default(), paths: &config.Paths{Downloads: t.TempDir()}}

	_, collector := newRunResolver(app, &fakeHistoryAPI{})
	collector.items = []store.MediaItem{
		{ChatID: 30, MessageID: 9, MediaClass: "photo", MediaID: 9}, // overlaps the manifest
		{ChatID: 30, MessageID: 40, MediaClass: "photo", MediaID: 40},
	}
	collector.incremental[30] = true

	merged, err := withCachedPending(ctx, state, []scan.Target{walkTestTarget(30, 0)}, collector)
	require.NoError(t, err)

	gotIDs := make([]int64, 0, len(merged))
	for _, item := range merged {
		gotIDs = append(gotIDs, item.MessageID)
	}

	assert.Equal(t, []int64{9, 40, 5}, gotIDs,
		"new items first, cached pending appended, done rows and duplicates excluded")
}
