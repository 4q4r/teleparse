package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"teleparse/internal/download"
	"teleparse/internal/filters"
	"teleparse/internal/scan"
	"teleparse/internal/store"
	tgap "teleparse/internal/tg"
	"testing"
	"time"

	tg "github.com/gotd/td/tg"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOutCmd returns a command whose stdout buffers into buf.
func newOutCmd() (*cobra.Command, *bytes.Buffer) {
	var buf bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	return cmd, &buf
}

// fakeRefetchAPI canned-answers the two message-fetch surfaces the resolver
// refetch path needs, tracking which one was used.
type fakeRefetchAPI struct {
	usedChannel bool
	usedUser    bool
	messages    tg.MessagesMessagesClass
	err         error
}

func (f *fakeRefetchAPI) MessagesGetMessages( //nolint:ireturn // the gotd union is the refetchAPI contract
	_ context.Context, _ []tg.InputMessageClass,
) (tg.MessagesMessagesClass, error) {
	f.usedUser = true

	return f.messages, f.err
}

func (f *fakeRefetchAPI) ChannelsGetMessages( //nolint:ireturn // the gotd union is the refetchAPI contract
	_ context.Context, _ *tg.ChannelsGetMessagesRequest,
) (tg.MessagesMessagesClass, error) {
	f.usedChannel = true

	return f.messages, f.err
}

func TestMessageCacheEvictsInInsertionOrder(t *testing.T) {
	t.Parallel()

	cache := newMessageCache(2)

	first := walkedMessage{fctx: filters.Context{Chat: filters.Chat{ID: 1}}}
	second := walkedMessage{fctx: filters.Context{Chat: filters.Chat{ID: 2}}}
	third := walkedMessage{fctx: filters.Context{Chat: filters.Chat{ID: 3}}}

	cache.put(msgCacheKey{chatID: 1, msgID: 10}, first)
	cache.put(msgCacheKey{chatID: 2, msgID: 20}, second)
	cache.put(msgCacheKey{chatID: 1, msgID: 10}, first)
	cache.put(msgCacheKey{chatID: 3, msgID: 30}, third)

	_, ok := cache.get(1, 10)
	assert.False(t, ok, "the oldest key is evicted even after a re-put")

	got, ok := cache.get(2, 20)
	require.True(t, ok)
	assert.Equal(t, int64(2), got.fctx.Chat.ID)

	got, ok = cache.get(3, 30)
	require.True(t, ok)
	assert.Equal(t, int64(3), got.fctx.Chat.ID)
}

func TestMediaItemFromMessageVariants(t *testing.T) {
	t.Parallel()

	baseFctx := func(file *filters.FileInfo) filters.Context {
		return filters.Context{
			Chat:    filters.Chat{ID: 7, Type: "channel", Title: "News"},
			Sender:  filters.Sender{Present: true, ID: 42, Name: "Ann"},
			Message: filters.Message{ID: 100, Date: 1750000000, GroupedID: 9},
			File:    file,
		}
	}

	photoMsg := &tg.Message{Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 555}}}

	item, ok := mediaItemFromMessage(baseFctx(&filters.FileInfo{
		Present: true, Kind: "photo", Size: 2048,
	}), photoMsg)
	require.True(t, ok)
	assert.Equal(t, int64(7), item.ChatID)
	assert.Equal(t, int64(100), item.MessageID)
	assert.Equal(t, "photo", item.MediaClass)
	assert.Equal(t, int64(555), item.MediaID)
	require.NotNil(t, item.Mime)
	assert.Equal(t, "image/jpeg", *item.Mime)
	require.NotNil(t, item.Filename)
	assert.Equal(t, "photo_555.jpg", *item.Filename)
	require.NotNil(t, item.Size)
	assert.Equal(t, int64(2048), *item.Size)
	require.NotNil(t, item.SenderID)
	assert.Equal(t, int64(42), *item.SenderID)
	require.NotNil(t, item.GroupedID)
	assert.Equal(t, int64(9), *item.GroupedID)
	require.NotNil(t, item.Date)
	assert.Equal(t, "2025-06-15T15:06:40Z", *item.Date)
	assert.Equal(t, store.StatusDiscovered, item.Status)

	docMsg := &tg.Message{
		ID:    101,
		Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 777, Size: 4096, MimeType: "video/mp4"}},
	}

	item, ok = mediaItemFromMessage(baseFctx(&filters.FileInfo{
		Present: true, Kind: "video", Name: "clip.mp4",
	}), docMsg)
	require.True(t, ok)
	assert.Equal(t, "video", item.MediaClass)
	assert.Equal(t, int64(777), item.MediaID)
	require.NotNil(t, item.Filename)
	assert.Equal(t, "clip.mp4", *item.Filename)
	require.NotNil(t, item.Mime)
	assert.Equal(t, "video/mp4", *item.Mime)

	item, ok = mediaItemFromMessage(baseFctx(&filters.FileInfo{
		Present: true, Kind: "document",
	}), docMsg)
	require.True(t, ok)
	require.NotNil(t, item.Filename)
	assert.Equal(t, "file_101", *item.Filename, "documents without a name fall back to the message id")

	for name, tc := range map[string]struct {
		file *filters.FileInfo
		msg  *tg.Message
	}{
		"no file context":  {file: nil, msg: docMsg},
		"file not present": {file: &filters.FileInfo{Kind: "video"}, msg: docMsg},
		"non-downloadable kind": {
			file: &filters.FileInfo{Present: true, Kind: "poll"},
			msg:  docMsg,
		},
		"unsupported media class": {
			file: &filters.FileInfo{Present: true, Kind: "video"},
			msg:  &tg.Message{Media: &tg.MessageMediaContact{}},
		},
		"photo placeholder": {
			file: &filters.FileInfo{Present: true, Kind: "photo"},
			msg:  &tg.Message{Media: &tg.MessageMediaPhoto{Photo: &tg.PhotoEmpty{}}},
		},
		"document placeholder": {
			file: &filters.FileInfo{Present: true, Kind: "video"},
			msg:  &tg.Message{Media: &tg.MessageMediaDocument{Document: &tg.DocumentEmpty{}}},
		},
		"no media at all": {
			file: &filters.FileInfo{Present: true, Kind: "video"},
			msg:  &tg.Message{},
		},
	} {
		_, ok := mediaItemFromMessage(baseFctx(tc.file), tc.msg)
		assert.False(t, ok, "%s must not produce a manifest row", name)
	}
}

func TestLocationFromMessageVariants(t *testing.T) {
	t.Parallel()

	photo := &tg.Message{Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID: 555, AccessHash: 90, FileReference: []byte("ref"),
		Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", Size: 640}},
	}}}

	loc, ok := locationFromMessage(photo).(*tg.InputPhotoFileLocation)
	require.True(t, ok, "photos resolve to a photo file location")
	assert.Equal(t, int64(555), loc.ID)
	assert.Equal(t, int64(90), loc.AccessHash)
	assert.Equal(t, "x", loc.ThumbSize)

	doc := &tg.Message{Media: &tg.MessageMediaDocument{Document: &tg.Document{
		ID: 777, AccessHash: 91, FileReference: []byte("ref2"),
	}}}

	docLoc, ok := locationFromMessage(doc).(*tg.InputDocumentFileLocation)
	require.True(t, ok, "documents resolve to a document file location")
	assert.Equal(t, int64(777), docLoc.ID)
	assert.Equal(t, int64(91), docLoc.AccessHash)

	assert.Nil(t, locationFromMessage(nil), "no message means no location")
	assert.Nil(t, locationFromMessage(&tg.Message{Media: &tg.MessageMediaGeo{}}),
		"geo messages carry no file location")
}

func TestThumbSizePicksLargestVariant(t *testing.T) {
	t.Parallel()

	mixed := &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "m", Size: 320},
		&tg.PhotoSizeProgressive{Type: "p", Sizes: []int{64, 1280}},
		&tg.PhotoSize{Type: "x", Size: 800},
	}}
	assert.Equal(t, "p", thumbSizeOf(mixed), "progressive sizes compete by their largest step")

	plain := &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "a", Size: 100},
		&tg.PhotoSize{Type: "b", Size: 200},
	}}
	assert.Equal(t, "b", thumbSizeOf(plain))

	assert.Equal(t, photoFallbackThumb, thumbSizeOf(&tg.Photo{}),
		"a photo without sizes falls back to the m thumb")
	assert.Zero(t, largestProgressive(nil))
}

func TestRunResolverResolveCacheHitAndMiss(t *testing.T) {
	t.Parallel()

	api := &fakeRefetchAPI{}
	resolver := &runResolver{
		root:     "root",
		template: "{chat}/{msgid}_{filename}",
		cache:    newMessageCache(8),
		peers:    map[int64]tg.InputPeerClass{},
		api:      api,
	}

	fctx := filters.Context{
		Chat:    filters.Chat{ID: 7, Type: "channel", Title: "News"},
		Sender:  filters.Sender{Present: true, ID: 42, Name: "Ann"},
		Message: filters.Message{ID: 100, Date: 1750000000, Text: "hello"},
		File:    &filters.FileInfo{Present: true, Kind: "document", Name: "a.bin", Ext: ".bin"},
	}

	resolver.cache.put(msgCacheKey{chatID: 7, msgID: 100}, walkedMessage{
		fctx: fctx,
		msg: &tg.Message{Media: &tg.MessageMediaDocument{Document: &tg.Document{
			ID: 777, AccessHash: 1, FileReference: []byte("r"),
		}}},
	})

	hit, err := resolver.Resolve(store.MediaItem{ChatID: 7, MessageID: 100})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("root", "News", "100_a.bin"), hit.Path)
	require.NotNil(t, hit.Meta)
	assert.Equal(t, "News", hit.Meta.ChatTitle)
	assert.Equal(t, "hello", hit.Meta.Text)
	assert.Equal(t, "a.bin", hit.Meta.Filename)
	assert.Equal(t, int64(42), hit.Meta.SenderID)
	require.NotNil(t, hit.Location, "the cached message supplies the file location")

	miss, err := resolver.Resolve(store.MediaItem{
		ChatID: 9, MessageID: 55, MediaIndex: 2,
		Filename: ptr("pic.png"), Date: ptr("2026-01-02T03:04:05Z"),
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("root", "9", "55_2.png"), miss.Path)
	require.NotNil(t, miss.Meta)
	assert.Equal(t, int64(9), miss.Meta.ChatID)
	assert.Equal(t, 2, miss.Meta.MediaIndex)
	assert.Equal(t, "pic.png", miss.Meta.Filename)
	assert.Equal(t, int64(1767323045), miss.Meta.Date)
	assert.Nil(t, miss.Location, "a cache miss has no location until refetch")
	require.NotNil(t, miss.Refetch, "the refetch hook is always attached")
}

func TestRunResolverRefetchPaths(t *testing.T) {
	t.Parallel()

	docMsg := &tg.Message{ID: 100, Media: &tg.MessageMediaDocument{Document: &tg.Document{
		ID: 777, AccessHash: 1, FileReference: []byte("r"),
	}}}

	item := store.MediaItem{ChatID: 7, MessageID: 100}

	noPeer := &runResolver{cache: newMessageCache(1), peers: map[int64]tg.InputPeerClass{}, api: &fakeRefetchAPI{}}

	noPeerResolved, err := noPeer.Resolve(item)
	require.NoError(t, err)

	_, err = noPeerResolved.Refetch(t.Context())
	require.Error(t, err)
	require.ErrorIs(t, err, errNoPeerForRefetch)

	channelAPI := &fakeRefetchAPI{
		messages: &tg.MessagesChannelMessages{Messages: []tg.MessageClass{docMsg}},
	}

	channel := &runResolver{
		cache: newMessageCache(1),
		peers: map[int64]tg.InputPeerClass{
			7: &tg.InputPeerChannel{ChannelID: 7, AccessHash: 10},
		},
		api: channelAPI,
	}

	channelResolved, err := channel.Resolve(item)
	require.NoError(t, err)

	loc, err := channelResolved.Refetch(t.Context())
	require.NoError(t, err)
	assert.True(t, channelAPI.usedChannel, "channel peers use channels.getMessages")
	require.IsType(t, &tg.InputDocumentFileLocation{}, loc)

	userAPI := &fakeRefetchAPI{
		messages: &tg.MessagesMessages{Messages: []tg.MessageClass{docMsg}},
	}

	user := &runResolver{
		cache: newMessageCache(1),
		peers: map[int64]tg.InputPeerClass{7: &tg.InputPeerUser{UserID: 7, AccessHash: 11}},
		api:   userAPI,
	}

	userResolved, err := user.Resolve(item)
	require.NoError(t, err)

	_, err = userResolved.Refetch(t.Context())
	require.NoError(t, err)
	assert.True(t, userAPI.usedUser, "non-channel peers use messages.getMessages")

	empty := &runResolver{
		cache: newMessageCache(1),
		peers: map[int64]tg.InputPeerClass{
			7: &tg.InputPeerChannel{ChannelID: 7, AccessHash: 10},
		},
		api: &fakeRefetchAPI{messages: &tg.MessagesChannelMessages{}},
	}

	emptyResolved, err := empty.Resolve(item)
	require.NoError(t, err)

	_, err = emptyResolved.Refetch(t.Context())
	require.Error(t, err)
	require.ErrorIs(t, err, errMessageNotReturned)

	broken := errors.New("rpc unavailable")

	failing := &runResolver{
		cache: newMessageCache(1),
		peers: map[int64]tg.InputPeerClass{
			7: &tg.InputPeerChannel{ChannelID: 7, AccessHash: 10},
		},
		api: &fakeRefetchAPI{err: broken},
	}

	failingResolved, err := failing.Resolve(item)
	require.NoError(t, err)

	_, err = failingResolved.Refetch(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, broken)
}

func TestStderrProgressRendersEveryNOutcomes(t *testing.T) {
	t.Parallel()

	var buf guardedBuffer

	progress := newStderrProgress(&buf, 2)

	progress.SetPhase("downloading")

	progress.Inc("bytes", 128)
	progress.Inc("downloaded", 1)
	progress.Inc("downloaded", 1)
	progress.Inc("skipped", 1)
	progress.Inc("skipped", 1)
	progress.Inc("failed", 1)

	out := buf.String()
	assert.Contains(t, out, "--- downloading ---")
	assert.Contains(t, out, "progress: downloaded=2 skipped=0 failed=0")
	assert.Contains(t, out, "progress: downloaded=2 skipped=2 failed=0")
	assert.NotContains(t, out, "failed=1\n", "the last lone outcome stays buffered")
}

// guardedBuffer serializes writes like the real stderr.
type guardedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (g *guardedBuffer) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.buf.Write(p)
}

func (g *guardedBuffer) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.buf.String()
}

func TestHumanBytesAndSizes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "0B", humanBytes(0))
	assert.Equal(t, "512B", humanBytes(512))
	assert.Equal(t, "2.0KiB", humanBytes(2048))
	assert.Equal(t, "1.5MiB", humanBytes(1536*1024))
	assert.Equal(t, "3.0GiB", humanBytes(3*1024*1024*1024))

	assert.Equal(t, "?", humanSize(nil))

	sized := int64(10)
	assert.Equal(t, "10B", humanSize(&sized))

	collector := &walkCollector{items: []store.MediaItem{
		{Size: &sized},
		{Size: nil},
	}}
	assert.Equal(t, "10B", humanTotalSize(collector))
}

func TestCountForFiltersByChat(t *testing.T) {
	t.Parallel()

	collector := &walkCollector{items: []store.MediaItem{
		{ChatID: 1}, {ChatID: 1}, {ChatID: 2},
	}}

	assert.Equal(t, 2, countFor(collector, 1))
	assert.Equal(t, 1, countFor(collector, 2))
	assert.Zero(t, countFor(collector, 3))
}

func TestPrintPlanCountsSummary(t *testing.T) {
	t.Parallel()

	size := int64(1024)
	name := "a.bin"

	collector := &walkCollector{items: []store.MediaItem{
		{ChatID: 1, MessageID: 10, MediaClass: "document", Size: &size, Filename: &name},
	}}

	cmd, buf := newOutCmd()
	require.NoError(t, printPlan(cmd, collector))

	out := buf.String()
	assert.Contains(t, out, "CHAT")
	assert.Contains(t, out, "MSG")
	assert.Contains(t, out, "document")
	assert.Contains(t, out, "1.0KiB")
	assert.Contains(t, out, "a.bin")
	assert.Contains(t, out, "total: 1 file(s), 1.0KiB")

	targets := []scan.Target{{Chat: filters.Chat{ID: 1, Title: "News"}}}

	cmd, buf = newOutCmd()
	require.NoError(t, printCounts(cmd, collector, targets))
	assert.Contains(t, buf.String(), "MATCHES")
	assert.Contains(t, buf.String(), "News")

	cmd, buf = newOutCmd()
	require.NoError(t, printSummary(cmd, download.Result{
		Downloaded: 2, Bytes: 3072, Skipped: 1, Failed: 1,
	}, 90*time.Second))
	assert.Contains(t, buf.String(), "downloaded: 2 (3.0KiB), skipped: 1, failed: 1")
}

func TestPrintChatsAndDetail(t *testing.T) {
	t.Parallel()

	chats := []tgap.ChatInfo{
		{ID: 1, Type: "channel", Title: "News"},
		{ID: 2, Type: "private", Title: "Ann"},
	}

	filtered := filterChatsByType(chats, map[string]bool{"channel": true})
	require.Len(t, filtered, 1)
	assert.Equal(t, "News", filtered[0].Title)

	cmd, buf := newOutCmd()
	require.NoError(t, printChats(cmd, chats, false))
	assert.Contains(t, buf.String(), "1")
	assert.Contains(t, buf.String(), "channel")
	assert.Contains(t, buf.String(), "News")

	cmd, buf = newOutCmd()
	require.NoError(t, printChats(cmd, nil, false))
	assert.Contains(t, buf.String(), "no chats")

	cmd, buf = newOutCmd()
	require.NoError(t, printChats(cmd, chats, true))
	assert.Contains(t, buf.String(), `"title": "Ann"`, "json mode marshals the full list")

	cmd, buf = newOutCmd()
	printChatDetail(cmd, tgap.ChatInfo{ID: 3, Title: "Docs", Type: "group", Username: "docs"})
	assert.Contains(t, buf.String(), "id:       3")
	assert.Contains(t, buf.String(), "username: @docs")
}

func TestEncodeRunPayloadRoundtrip(t *testing.T) {
	t.Parallel()

	opts := filters.Options{Dedupe: "unique-id", Media: []string{"photo", "video"}}

	encoded, err := encodeRunPayload([]string{"all", "@news"}, opts)
	require.NoError(t, err)

	var decoded runPayload
	require.NoError(t, toml.Unmarshal([]byte(encoded), &decoded))

	assert.Equal(t, []string{"all", "@news"}, decoded.Chats)
	assert.Equal(t, "unique-id", decoded.Options.Dedupe)
	assert.Equal(t, []string{"photo", "video"}, decoded.Options.Media)
}

func TestNewRunIDShapeAndUniqueness(t *testing.T) {
	t.Parallel()

	shape := regexp.MustCompile(`^run-\d{8}-\d{6}-[0-9a-f]{8}$`)

	seen := map[string]struct{}{}
	for range 32 {
		id, err := newRunID()
		require.NoError(t, err)
		require.Regexp(t, shape, id)
		seen[id] = struct{}{}
	}

	assert.Len(t, seen, 32, "the salt keeps run ids collision-free")
}

func TestSmallHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1500*time.Millisecond, secondsDuration(1.5))
	assert.Nil(t, stringOrNil(""))
	require.NotNil(t, stringOrNil("x"))
	assert.Equal(t, "x", *stringOrNil("x"))
	assert.Empty(t, textOrDefault(nil))
	assert.Equal(t, "v", textOrDefault(ptr("v")))
	assert.Equal(t, "7", itoa(7))
}

func ptr[T any](v T) *T { return &v }
