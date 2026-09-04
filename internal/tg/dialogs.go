package tg

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query/dialogs"
	gotdtg "github.com/gotd/td/tg"
)

// ChatInfo is the normalized chat record used by chats list/show and the scan
// scope resolver. Type is one of the ChatType* constants.
type ChatInfo struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Username  string `json:"username,omitempty"`
	Archived  bool   `json:"archived"`
	Protected bool   `json:"protected"`
}

// Chats iterates all dialogs visible to the account. The archived filter takes
// "only", "exclude" or "any"/"" and maps onto the dialog folder flag.
func Chats(ctx context.Context, client *telegram.Client, archived string) ([]ChatInfo, error) {
	mode := strings.ToLower(archived)

	switch mode {
	case "only", "exclude", "", "any":
	default:
		return nil, fmt.Errorf("archived %q: %w", archived, ErrBadArchivedFilter)
	}

	iterator := dialogs.
		NewQueryBuilder(client.API()).
		GetDialogs().
		BatchSize(dialogBatchSize).
		Iter()

	chats := make([]ChatInfo, 0)

	for iterator.Next(ctx) {
		elem := iterator.Value()

		dialog, ok := elem.Dialog.(*gotdtg.Dialog)
		if !ok {
			continue
		}

		info, ok := ChatInfoFromElem(dialog, elem.Entities)
		if !ok {
			continue
		}

		if !archivedMatches(mode, info.Archived) {
			continue
		}

		chats = append(chats, info)
	}

	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("iterate dialogs: %w", err)
	}

	return chats, nil
}

// Contacts returns the account's contact list as private chats.
func Contacts(ctx context.Context, client *telegram.Client) ([]ChatInfo, error) {
	result, err := client.API().ContactsGetContacts(ctx, contactsHashZero)
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}

	list, ok := result.(*gotdtg.ContactsContacts)
	if !ok {
		return nil, nil
	}

	chats := make([]ChatInfo, 0, len(list.Users))

	for _, userClass := range list.Users {
		user, ok := userClass.(*gotdtg.User)
		if !ok {
			continue
		}

		chats = append(chats, ChatInfo{
			ID:       user.ID,
			Title:    strings.TrimSpace(user.FirstName + " " + user.LastName),
			Type:     ChatTypePrivate,
			Username: user.Username,
		})
	}

	return chats, nil
}

// FindChat resolves a chat reference: @username (or a bare name) via the
// server resolver, or a numeric id (with optional -100 channel prefix) by
// scanning dialogs for the access-hash-bearing entity.
func FindChat(ctx context.Context, client *telegram.Client, ref string) (*ChatInfo, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(ref), "@")

	if clean == "" {
		return nil, fmt.Errorf("%q: %w: empty reference", ref, ErrChatNotFound)
	}

	if numeric, err := strconv.ParseInt(strings.TrimPrefix(clean, "-100"), 10, 64); err == nil {
		return findChatByID(ctx, client, numeric)
	}

	resolved, err := client.API().ContactsResolveUsername(ctx, &gotdtg.ContactsResolveUsernameRequest{
		Username: clean,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w: %w", clean, ErrChatNotFound, err)
	}

	entities := peer.EntitiesFromResult(resolved)

	dialog := &gotdtg.Dialog{Peer: resolved.Peer}

	if info, ok := ChatInfoFromElem(dialog, entities); ok {
		return &info, nil
	}

	return nil, fmt.Errorf("%q: %w", ref, ErrChatNotFound)
}

// findChatByID scans dialogs until one matches the numeric id.
func findChatByID(ctx context.Context, client *telegram.Client, id int64) (*ChatInfo, error) {
	chats, err := Chats(ctx, client, archivedAny)
	if err != nil {
		return nil, err
	}

	for _, info := range chats {
		if info.ID == id {
			found := info

			return &found, nil
		}
	}

	return nil, fmt.Errorf("id %d: %w", id, ErrChatNotFound)
}

// ChatInfoFromElem maps one dialog plus its entities onto ChatInfo, mapping
// peer types honestly: users split into private/bot, chats into group,
// channels into supergroup/forum (megagroup) or channel (broadcast, gigagroup).
// The second return is false when the entity data is missing.
func ChatInfoFromElem(dialog *gotdtg.Dialog, entities peer.Entities) (ChatInfo, bool) {
	var info ChatInfo

	info.Archived = dialog.FolderID == archivedFolderID

	switch peerType := dialog.Peer.(type) {
	case *gotdtg.PeerUser:
		user, ok := entities.User(peerType.UserID)
		if !ok {
			return info, false
		}

		info.ID = user.ID
		info.Title = strings.TrimSpace(user.FirstName + " " + user.LastName)
		info.Username = user.Username

		if user.Bot {
			info.Type = ChatTypeBot
		} else {
			info.Type = ChatTypePrivate
		}
	case *gotdtg.PeerChat:
		chat, ok := entities.Chat(peerType.ChatID)
		if !ok {
			return info, false
		}

		info.ID = chat.ID
		info.Title = chat.Title
		info.Type = ChatTypeGroup
		info.Protected = chat.Noforwards
	case *gotdtg.PeerChannel:
		channel, ok := entities.Channel(peerType.ChannelID)
		if !ok {
			return info, false
		}

		info.ID = channel.ID
		info.Title = channel.Title
		info.Username = channel.Username
		info.Protected = channel.Noforwards

		info.Type = classifyChannel(channel)
	default:
		return info, false
	}

	return info, true
}

// classifyChannel maps channel flags onto the chat type vocabulary.
func classifyChannel(channel *gotdtg.Channel) string {
	switch {
	case channel.Forum:
		return ChatTypeForum
	case channel.Megagroup:
		return ChatTypeSupergroup
	default:
		return ChatTypeChannel
	}
}

// archivedMatches applies the archived filter mode.
func archivedMatches(mode string, archived bool) bool {
	switch mode {
	case "only":
		return archived
	case "exclude":
		return !archived
	default:
		return true
	}
}

// Batch size for dialog iteration and the contacts hash sentinel.
const (
	dialogBatchSize  = 100
	contactsHashZero = 0
	archivedAny      = "any"
)
