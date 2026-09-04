package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"sync"
	"teleparse/internal/download"
	"teleparse/internal/filters"
	"teleparse/internal/store"
	"time"

	tg "github.com/gotd/td/tg"
)

// Sentinel errors for message-context resolution.
var (
	errNoPeerForRefetch   = errors.New("chat peer unknown; refetch impossible without a fresh walk")
	errMessageNotReturned = errors.New("server returned no message for id")
)

// msgCacheLimit bounds the retained walked messages (LRU by insertion).
const msgCacheLimit = 10_000

// photoFallbackThumb is the size used when a photo exposes no sizes.
const photoFallbackThumb = "m"

// msgCacheKey identifies one walked message.
type msgCacheKey struct {
	chatID int64
	msgID  int64
}

// walkedMessage retains what the download stage needs after the walk: the
// filter context (paths, sidecars) plus the raw message (file references).
type walkedMessage struct {
	fctx filters.Context
	msg  *tg.Message
}

// messageCache is a bounded (chat, message) map evicting in insertion
// order; workers only need recent messages because downloads interleave
// with the walk per target.
type messageCache struct {
	limit   int
	mu      sync.Mutex
	entries map[msgCacheKey]walkedMessage
	order   []msgCacheKey
}

func newMessageCache(limit int) *messageCache {
	return &messageCache{limit: limit, entries: map[msgCacheKey]walkedMessage{}}
}

func (c *messageCache) put(key msgCacheKey, entry walkedMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}

	c.entries[key] = entry

	for len(c.order) > c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]

		delete(c.entries, oldest)
	}
}

func (c *messageCache) get(chatID, msgID int64) (walkedMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[msgCacheKey{chatID: chatID, msgID: msgID}]

	return entry, ok
}

// refetchAPI is the raw message surface file-reference refetch needs; the
// *tg.Client over a live or takeout-wrapped invoker satisfies it.
type refetchAPI interface {
	MessagesGetMessages(ctx context.Context, id []tg.InputMessageClass) (tg.MessagesMessagesClass, error)
	ChannelsGetMessages(ctx context.Context, request *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error)
}

// runResolver renders final paths, sidecar metadata and file locations from
// the walked message cache, falling back to store-only fields on cache
// misses (resume without fresh walk context). It implements
// download.ItemResolver.
type runResolver struct {
	root     string
	template string
	cache    *messageCache
	peers    map[int64]tg.InputPeerClass
	api      refetchAPI
}

// Resolve implements download.ItemResolver for one media row.
func (r *runResolver) Resolve(item store.MediaItem) (download.Resolved, error) {
	resolved := download.Resolved{Refetch: r.refetchFor(item)}

	if entry, ok := r.cache.get(item.ChatID, item.MessageID); ok {
		rel, err := download.RenderTemplate(r.template, entry.fctx, entry.fctx.File)
		if err != nil {
			return download.Resolved{}, fmt.Errorf("render path for %d/%d: %w", item.ChatID, item.MessageID, err)
		}

		resolved.Path = path.Join(r.root, rel)
		resolved.Meta = sidecarFromContext(entry.fctx, resolved.Path)
		resolved.Location = locationFromMessage(entry.msg)
		resolved.DC = dcFromMessage(entry.msg)

		return resolved, nil
	}

	resolved.Path = path.Join(r.root, fallbackRelPath(item))
	resolved.Meta = sidecarFromItem(item, resolved.Path)

	return resolved, nil
}

func (r *runResolver) refetchFor(item store.MediaItem) download.RefetchFunc {
	return func(ctx context.Context) (tg.InputFileLocationClass, error) {
		msg, err := r.refetchMessage(ctx, item.ChatID, item.MessageID)
		if err != nil {
			return nil, fmt.Errorf("refetch %d/%d: %w", item.ChatID, item.MessageID, err)
		}

		location := locationFromMessage(msg)
		if location == nil {
			return nil, fmt.Errorf("message %d/%d carries no downloadable media: %w",
				item.ChatID, item.MessageID, errMessageNotReturned)
		}

		return location, nil
	}
}

func (r *runResolver) refetchMessage(ctx context.Context, chatID, msgID int64) (*tg.Message, error) {
	peer, ok := r.peers[chatID]
	if !ok {
		return nil, fmt.Errorf("chat %d: %w", chatID, errNoPeerForRefetch)
	}

	request := []tg.InputMessageClass{&tg.InputMessageID{ID: int(msgID)}}

	var (
		result tg.MessagesMessagesClass
		err    error
	)

	if channel, isChannel := peer.(*tg.InputPeerChannel); isChannel {
		result, err = r.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      request,
		})
		if err != nil {
			return nil, fmt.Errorf("channels.getMessages: %w", err)
		}
	} else {
		result, err = r.api.MessagesGetMessages(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("messages.getMessages: %w", err)
		}
	}

	for _, candidate := range messagesOfClass(result) {
		if int64(candidate.ID) == msgID {
			return candidate, nil
		}
	}

	return nil, fmt.Errorf("message %d in chat %d: %w", msgID, chatID, errMessageNotReturned)
}

func messagesOfClass(result tg.MessagesMessagesClass) []*tg.Message {
	var messages []*tg.Message

	switch typed := result.(type) {
	case *tg.MessagesMessages:
		messages = filterMessages(typed.Messages)
	case *tg.MessagesMessagesSlice:
		messages = filterMessages(typed.Messages)
	case *tg.MessagesChannelMessages:
		messages = filterMessages(typed.Messages)
	default:
	}

	return messages
}

func filterMessages(class []tg.MessageClass) []*tg.Message {
	messages := make([]*tg.Message, 0, len(class))

	for _, entry := range class {
		if msg, ok := entry.(*tg.Message); ok {
			messages = append(messages, msg)
		}
	}

	return messages
}

// locationFromMessage builds the download location from the raw message
// media, returning nil when the message carries no downloadable file.
//
//nolint:ireturn // the gotd location union is the downloader input contract
func locationFromMessage(msg *tg.Message) tg.InputFileLocationClass {
	if msg == nil {
		return nil
	}

	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok {
			return nil
		}

		return &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     thumbSizeOf(photo),
		}
	case *tg.MessageMediaDocument:
		document, ok := media.Document.(*tg.Document)
		if !ok {
			return nil
		}

		return &tg.InputDocumentFileLocation{
			ID:            document.ID,
			AccessHash:    document.AccessHash,
			FileReference: document.FileReference,
		}
	default:
		return nil
	}
}

// dcFromMessage extracts the data center a message's file is stored on;
// 0 when unknown, which routes the transfer through the home pool and the
// FILE_MIGRATE retry.
func dcFromMessage(msg *tg.Message) int {
	if msg == nil {
		return 0
	}

	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		if photo, ok := media.Photo.(*tg.Photo); ok {
			return photo.DCID
		}
	case *tg.MessageMediaDocument:
		if document, ok := media.Document.(*tg.Document); ok {
			return document.DCID
		}
	default:
	}

	return 0
}

// thumbSizeOf picks the type tag of the largest photo size.
func thumbSizeOf(photo *tg.Photo) string {
	best, bestBytes := "", 0

	for _, variant := range photo.Sizes {
		switch sized := variant.(type) {
		case *tg.PhotoSize:
			if sized.Size > bestBytes {
				best, bestBytes = sized.Type, sized.Size
			}
		case *tg.PhotoSizeProgressive:
			if last := largestProgressive(sized.Sizes); last > bestBytes {
				best, bestBytes = sized.Type, last
			}
		default:
		}
	}

	if best == "" {
		return photoFallbackThumb
	}

	return best
}

func largestProgressive(sizes []int) int {
	largest := 0

	for _, candidate := range sizes {
		if candidate > largest {
			largest = candidate
		}
	}

	return largest
}

// sidecarFromContext projects the walked message context onto sidecar
// metadata.
func sidecarFromContext(fctx filters.Context, finalPath string) *download.SidecarMeta {
	meta := &download.SidecarMeta{
		ChatID:     fctx.Chat.ID,
		ChatTitle:  fctx.Chat.Title,
		ChatType:   fctx.Chat.Type,
		MessageID:  fctx.Message.ID,
		MediaIndex: 0,
		Date:       fctx.Message.Date,
		Text:       fctx.TextOrCaption(),
		Entities:   fctx.Message.Entities,
		File:       fctx.File,
		Path:       finalPath,
	}

	if fctx.Sender.Present {
		meta.SenderID = fctx.Sender.ID
		meta.SenderName = fctx.Sender.Name
	}

	if fctx.File != nil {
		meta.Filename = fctx.File.Name
	}

	return meta
}

// sidecarFromItem projects store-only fields onto sidecar metadata for
// resumed rows whose walk context is gone.
func sidecarFromItem(item store.MediaItem, finalPath string) *download.SidecarMeta {
	meta := &download.SidecarMeta{
		ChatID:     item.ChatID,
		MessageID:  item.MessageID,
		MediaIndex: item.MediaIndex,
		Path:       finalPath,
	}

	if item.Date != nil {
		if stamped, err := time.Parse(time.RFC3339, *item.Date); err == nil {
			meta.Date = stamped.Unix()
		}
	}

	meta.Filename = textOrDefault(item.Filename)

	return meta
}

// fallbackRelPath derives a safe path when no template context exists:
// <chatID>/<msgID>_<index><ext>.
func fallbackRelPath(item store.MediaItem) string {
	name := strconv.FormatInt(item.MessageID, 10) + "_" + strconv.Itoa(item.MediaIndex)

	ext := ""
	if item.Filename != nil {
		ext = path.Ext(*item.Filename)
	}

	return path.Join(strconv.FormatInt(item.ChatID, 10), name+ext)
}

func textOrDefault(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
