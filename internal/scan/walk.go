package scan

import (
	"context"
	"fmt"
	"teleparse/internal/filters"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/query/messages"
	tg "github.com/gotd/td/tg"
)

// WalkAPI is the raw history surface walking needs; the *tg.Client returned
// by (*telegram.Client).API satisfies it.
type WalkAPI interface {
	MessagesGetHistory(ctx context.Context, request *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	MessagesSearch(ctx context.Context, request *tg.MessagesSearchRequest) (tg.MessagesMessagesClass, error)
	MessagesGetReplies(ctx context.Context, request *tg.MessagesGetRepliesRequest) (tg.MessagesMessagesClass, error)
	MessagesGetForumTopics(
		ctx context.Context, request *tg.MessagesGetForumTopicsRequest,
	) (*tg.MessagesForumTopics, error)
}

// EmitFunc receives every message that passes the compiled predicates. The
// raw gotd message is nil for service messages, which carry no downloadable
// media.
type EmitFunc func(mctx filters.Context, msg *tg.Message) error

// Message batch and topic page sizes.
const (
	walkBatchSize  = 100
	topicsPageSize = 100
)

// FeedRequest describes one message iteration: the peer, the server-side
// pushdown constraints and, for forum topic threads, the root message id.
type FeedRequest struct {
	Peer    tg.InputPeerClass
	Filter  tg.MessagesFilterClass
	Query   string
	MinDate int64
	MaxDate int64
	MinID   int64
	MaxID   int64
	MsgID   int
}

// FeedFactory builds one message feed for a walk request.
type FeedFactory func(req FeedRequest) *messages.Iterator

// HistoryWalker walks chat history, mapping gotd messages onto the filters
// model and emitting every message that passes the compiled predicates.
type HistoryWalker struct {
	api     WalkAPI
	feeds   FeedFactory
	senders *SenderCache
}

// NewHistoryWalker returns a walker over the given API, feed factory and
// sender cache.
func NewHistoryWalker(api WalkAPI, feeds FeedFactory, senders *SenderCache) *HistoryWalker {
	return &HistoryWalker{api: api, feeds: feeds, senders: senders}
}

// PushdownFilter maps the plan's MessagesFilter vocabulary onto gotd server
// filter types. Unknown or empty names return nil, meaning no server-side
// filtering.
//
//nolint:ireturn // sixteen concrete filter types share one interface result
func PushdownFilter(name string) tg.MessagesFilterClass {
	switch name {
	case "photo":
		return &tg.InputMessagesFilterPhotos{}
	case "video":
		return &tg.InputMessagesFilterVideo{}
	case "photo_video":
		return &tg.InputMessagesFilterPhotoVideo{}
	case "document":
		return &tg.InputMessagesFilterDocument{}
	case "url":
		return &tg.InputMessagesFilterURL{}
	case "gif":
		return &tg.InputMessagesFilterGif{}
	case "voice":
		return &tg.InputMessagesFilterVoice{}
	case "music":
		return &tg.InputMessagesFilterMusic{}
	case "round_video":
		return &tg.InputMessagesFilterRoundVideo{}
	case "round_voice":
		return &tg.InputMessagesFilterRoundVoice{}
	case "geo":
		return &tg.InputMessagesFilterGeo{}
	case "contact":
		return &tg.InputMessagesFilterContacts{}
	case "pinned":
		return &tg.InputMessagesFilterPinned{}
	case "chat_photos":
		return &tg.InputMessagesFilterChatPhotos{}
	case "phone_calls":
		return &tg.InputMessagesFilterPhoneCalls{}
	case "my_mentions":
		return &tg.InputMessagesFilterMyMentions{}
	default:
		return nil
	}
}

// Walk walks one target. With Recursion.Topics on a forum target, every forum
// topic thread is walked instead of the flat history; the server-side media
// filter stays inactive there because getReplies has no filter parameter.
// opts.Limit bounds scanned messages per target (0 walks everything).
// opts.Reverse buffers matches and emits oldest-first.
func (w *HistoryWalker) Walk(
	ctx context.Context, target Target, plan *filters.Plan, opts filters.Options, emit EmitFunc,
) error {
	emitter := newEmitter(emit, opts.Reverse)
	state := &walkState{limit: opts.Limit}

	if opts.Recursion.Topics && target.Chat.Type == chatForum {
		topics, err := w.forumTopics(ctx, target)
		if err != nil {
			return err
		}

		for _, topicID := range topics {
			req := FeedRequest{
				Peer: target.InputPeer, MsgID: topicID,
				MinID: opts.MinID, MaxID: opts.MaxID,
			}

			if err := w.runFeed(ctx, w.feeds(req), target, plan, emitter, state); err != nil {
				return err
			}

			if state.done() {
				break
			}
		}
	} else {
		if err := w.runFeed(ctx, w.feeds(historyRequest(target, plan, opts)), target, plan, emitter, state); err != nil {
			return err
		}
	}

	return emitter.flush()
}

// walkState tracks per-target scan budget.
type walkState struct {
	limit   int
	scanned int
}

func (s *walkState) done() bool {
	return s.limit > 0 && s.scanned >= s.limit
}

func historyRequest(target Target, plan *filters.Plan, opts filters.Options) FeedRequest {
	return FeedRequest{
		Peer:    target.InputPeer,
		Filter:  PushdownFilter(plan.Pushdown.MessagesFilter),
		Query:   plan.Pushdown.SearchQuery,
		MinDate: plan.Pushdown.MinDate,
		MaxDate: plan.Pushdown.MaxDate,
		MinID:   opts.MinID,
		MaxID:   opts.MaxID,
	}
}

func (w *HistoryWalker) runFeed(
	ctx context.Context, feed *messages.Iterator, target Target,
	plan *filters.Plan, emitter *matchEmitter, state *walkState,
) error {
	for feed.Next(ctx) {
		if state.done() {
			return nil
		}

		state.scanned++

		elem := feed.Value()

		switch typed := elem.Msg.(type) {
		case *tg.Message:
			if err := w.emitMessage(ctx, elem, typed, target, plan, emitter); err != nil {
				return err
			}
		case *tg.MessageService:
			if err := w.emitService(ctx, elem, typed, target, plan, emitter); err != nil {
				return err
			}
		default:
		}
	}

	if err := feed.Err(); err != nil {
		return fmt.Errorf("iterate history of %q: %w", target.Chat.Title, err)
	}

	return nil
}

func (w *HistoryWalker) emitMessage(
	ctx context.Context, elem messages.Elem, msg *tg.Message, target Target,
	plan *filters.Plan, emitter *matchEmitter,
) error {
	mctx := MapMessage(msg, target.Chat, w.senderFor(ctx, elem, msg))

	if !matches(plan, &mctx) {
		return nil
	}

	return emitter.add(mctx, msg)
}

func (w *HistoryWalker) emitService(
	ctx context.Context, elem messages.Elem, service *tg.MessageService, target Target,
	plan *filters.Plan, emitter *matchEmitter,
) error {
	mctx := MapService(service, target.Chat, w.senderFor(ctx, elem, service))

	if !matches(plan, &mctx) {
		return nil
	}

	return emitter.add(mctx, nil)
}

// senderFor resolves the message author, seeding the cache from the batch
// entities first. Resolution failures degrade to an absent sender, so only
// unset tri-bool predicates match.
func (w *HistoryWalker) senderFor(ctx context.Context, elem messages.Elem, msg tg.NotEmptyMessage) filters.Sender {
	w.senders.Seed(elem.Entities)

	fromID, ok := msg.GetFromID()
	if !ok {
		return filters.Sender{}
	}

	sender, err := w.senders.Get(ctx, fromID)
	if err != nil {
		return filters.Sender{}
	}

	return sender
}

func matches(plan *filters.Plan, mctx *filters.Context) bool {
	for _, predicate := range plan.Predicates {
		if !predicate.Fn(mctx) {
			return false
		}
	}

	return true
}

func (w *HistoryWalker) forumTopics(ctx context.Context, target Target) ([]int, error) {
	topics := make([]int, 0)

	for offsetTopic := 0; ; {
		page, err := w.api.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
			Peer: target.InputPeer, OffsetTopic: offsetTopic, Limit: topicsPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("list forum topics of %q: %w", target.Chat.Title, err)
		}

		for _, topicClass := range page.Topics {
			topic, ok := topicClass.(*tg.ForumTopic)
			if !ok {
				continue
			}

			topics = append(topics, topic.ID)
		}

		if len(page.Topics) < topicsPageSize {
			return topics, nil
		}

		if last := topics[len(topics)-1]; last > 0 {
			offsetTopic = last
		}
	}
}

// matchEmitter streams matches, or buffers them for reversed emission.
type matchEmitter struct {
	emit    EmitFunc
	reverse bool
	buffer  []matchEntry
}

type matchEntry struct {
	mctx filters.Context
	msg  *tg.Message
}

func newEmitter(emit EmitFunc, reverse bool) *matchEmitter {
	return &matchEmitter{emit: emit, reverse: reverse}
}

func (m *matchEmitter) add(mctx filters.Context, msg *tg.Message) error {
	if !m.reverse {
		return m.emit(mctx, msg)
	}

	m.buffer = append(m.buffer, matchEntry{mctx: mctx, msg: msg})

	return nil
}

func (m *matchEmitter) flush() error {
	if !m.reverse {
		return nil
	}

	for idx := len(m.buffer) - 1; idx >= 0; idx-- {
		entry := m.buffer[idx]

		if err := m.emit(entry.mctx, entry.msg); err != nil {
			return fmt.Errorf("emit match: %w", err)
		}
	}

	return nil
}

// feedQuery adapts FeedRequest onto the gotd iterator Query seam, issuing raw
// messages.getHistory, messages.search or messages.getReplies calls with full
// control over the id, date and filter parameters.
//
// core.telegram.org/api/offsets: the effective offset is
// offsetFromID(offset_id) + add_offset, so AddOffset<0 shifts the window
// toward NEWER messages; gotd's iterator owns that sign while walking
// newest-to-oldest, and min_id/max_id filter ids strictly greater/less.
type feedQuery struct {
	api WalkAPI
	req FeedRequest
}

//nolint:ireturn // implements the gotd messages.Query interface
func (q feedQuery) Query(ctx context.Context, req messages.Request) (tg.MessagesMessagesClass, error) {
	switch {
	case q.req.MsgID > 0:
		result, err := q.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer:       q.req.Peer,
			MsgID:      q.req.MsgID,
			OffsetID:   req.OffsetID,
			OffsetDate: req.OffsetDate,
			AddOffset:  req.AddOffset,
			Limit:      req.Limit,
			MaxID:      int(q.req.MaxID),
			MinID:      int(q.req.MinID),
		})
		if err != nil {
			return nil, fmt.Errorf("fetch topic replies: %w", err)
		}

		return result, nil
	case q.req.Filter != nil || q.req.Query != "" || q.req.MinDate != 0 || q.req.MaxDate != 0:
		result, err := q.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer:      q.req.Peer,
			Q:         q.req.Query,
			Filter:    q.req.Filter,
			MinDate:   int(q.req.MinDate),
			MaxDate:   int(q.req.MaxDate),
			OffsetID:  req.OffsetID,
			AddOffset: req.AddOffset,
			Limit:     req.Limit,
			MaxID:     int(q.req.MaxID),
			MinID:     int(q.req.MinID),
		})
		if err != nil {
			return nil, fmt.Errorf("search history: %w", err)
		}

		return result, nil
	default:
		result, err := q.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:       q.req.Peer,
			OffsetID:   req.OffsetID,
			OffsetDate: req.OffsetDate,
			AddOffset:  req.AddOffset,
			Limit:      req.Limit,
			MaxID:      int(q.req.MaxID),
			MinID:      int(q.req.MinID),
		})
		if err != nil {
			return nil, fmt.Errorf("fetch history: %w", err)
		}

		return result, nil
	}
}

// HistoryFeeds returns the production feed factory over the raw API.
func HistoryFeeds(api WalkAPI) FeedFactory {
	return func(req FeedRequest) *messages.Iterator {
		return messages.NewIterator(feedQuery{api: api, req: req}, walkBatchSize)
	}
}

// WalkClient walks one target against a live telegram client using the
// production feed factory and sender cache.
func WalkClient(
	ctx context.Context, client *telegram.Client, target Target,
	plan *filters.Plan, opts filters.Options, emit EmitFunc,
) error {
	api := client.API()
	walker := NewHistoryWalker(api, HistoryFeeds(api), NewSenderCache(api))

	if err := walker.Walk(ctx, target, plan, opts, emit); err != nil {
		return fmt.Errorf("walk %q: %w", target.Chat.Title, err)
	}

	return nil
}
