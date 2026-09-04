package cli

import (
	"strconv"
	"teleparse/internal/filters"
	"teleparse/internal/store"
	"time"

	tg "github.com/gotd/td/tg"
)

// photoMime is the MIME type Telegram serves photos as.
const photoMime = "image/jpeg"

// mediaItemFromMessage projects one walked message onto a manifest row;
// ok is false for messages whose media is not downloadable server-side.
func mediaItemFromMessage(fctx filters.Context, msg *tg.Message) (store.MediaItem, bool) {
	file := fctx.File
	if file == nil || !file.Present {
		return store.MediaItem{}, false
	}

	item := store.MediaItem{
		ChatID:    fctx.Chat.ID,
		MessageID: fctx.Message.ID,
		Date:      stringPtr(time.Unix(fctx.Message.Date, 0).UTC().Format(time.RFC3339)),
		Status:    store.StatusDiscovered,
	}

	if fctx.Sender.Present {
		item.SenderID = int64Ptr(fctx.Sender.ID)
	}

	if fctx.Message.GroupedID != 0 {
		item.GroupedID = int64Ptr(fctx.Message.GroupedID)
	}

	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, valid := media.Photo.(*tg.Photo)
		if !valid {
			return store.MediaItem{}, false
		}

		item.MediaClass = "photo"
		item.MediaID = photo.ID
		item.Mime = stringPtr(photoMime)

		if file.Size > 0 {
			item.Size = int64Ptr(file.Size)
		}

		item.Filename = stringPtr("photo_" + itoa(photo.ID) + ".jpg")
	case *tg.MessageMediaDocument:
		document, valid := media.Document.(*tg.Document)
		if !valid {
			return store.MediaItem{}, false
		}

		if !downloadableKind(file.Kind) {
			return store.MediaItem{}, false
		}

		item.MediaClass = file.Kind
		item.MediaID = document.ID
		item.Mime = stringPtr(document.MimeType)
		item.Size = int64Ptr(document.Size)

		if file.Name != "" {
			item.Filename = stringPtr(file.Name)
		} else {
			item.Filename = stringPtr("file_" + itoa(int64(msg.ID)))
		}
	default:
		return store.MediaItem{}, false
	}

	return item, true
}

// downloadableKind reports the document-backed kinds the pipeline can fetch
// by id; everything else (polls, geo, webpages...) has no file location.
func downloadableKind(kind string) bool {
	switch kind {
	case "video", "video-note", "voice", "audio", "document", "sticker", "gif":
		return true
	default:
		return false
	}
}

func stringPtr(text string) *string { return &text }
func int64Ptr(value int64) *int64   { return &value }

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
