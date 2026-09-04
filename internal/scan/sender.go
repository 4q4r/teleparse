package scan

import (
	"context"
	"fmt"
	"strings"
	"teleparse/internal/filters"

	"github.com/gotd/td/telegram/message/peer"
	tg "github.com/gotd/td/tg"
)

// SenderAPI is the raw surface sender resolution needs; the *tg.Client
// returned by (*telegram.Client).API satisfies it.
type SenderAPI interface {
	UsersGetUsers(ctx context.Context, id []tg.InputUserClass) ([]tg.UserClass, error)
	ChannelsGetChannels(ctx context.Context, id []tg.InputChannelClass) (tg.MessagesChatsClass, error)
}

// SenderCache lazily resolves and caches message senders by peer id. Peers
// found in a message batch's entity set are seeded for free; unknown peers
// are resolved through the API once, and failures degrade to an absent sender
// so predicates only match on unset tri-bools.
type SenderCache struct {
	api      SenderAPI
	users    map[int64]filters.Sender
	channels map[int64]filters.Sender
	missed   map[int64]bool
}

// NewSenderCache returns an empty cache resolving through api.
func NewSenderCache(api SenderAPI) *SenderCache {
	return &SenderCache{
		api:      api,
		users:    map[int64]filters.Sender{},
		channels: map[int64]filters.Sender{},
		missed:   map[int64]bool{},
	}
}

// Seed fills cache entries still missing from an entity set carried by a
// message batch. Already-known ids are skipped, so repeated seeding of the
// same batch costs one map lookup per entity.
func (c *SenderCache) Seed(entities peer.Entities) {
	for id, user := range entities.Users() {
		if _, ok := c.users[id]; !ok {
			c.users[id] = mapUserSender(user)
		}
	}

	for id, channel := range entities.Channels() {
		if _, ok := c.channels[id]; !ok {
			c.channels[id] = mapChannelSender(channel)
		}
	}
}

// Get resolves one user or channel peer into a Sender. Resolution failures
// return the wrapped error together with an absent Sender; the miss is
// negatively cached so subsequent lookups do not hammer the API.
func (c *SenderCache) Get(ctx context.Context, peer tg.PeerClass) (filters.Sender, error) {
	switch typed := peer.(type) {
	case *tg.PeerUser:
		if sender, ok := c.users[typed.UserID]; ok {
			return sender, nil
		}

		return c.resolveUser(ctx, typed.UserID)
	case *tg.PeerChannel:
		if sender, ok := c.channels[typed.ChannelID]; ok {
			return sender, nil
		}

		return c.resolveChannel(ctx, typed.ChannelID)
	case *tg.PeerChat:
		return filters.Sender{}, nil
	default:
		return filters.Sender{}, nil
	}
}

func (c *SenderCache) resolveUser(ctx context.Context, userID int64) (filters.Sender, error) {
	if c.missed[userID] {
		return filters.Sender{}, nil
	}

	users, err := c.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: userID}})
	if err != nil {
		c.missed[userID] = true

		return filters.Sender{}, fmt.Errorf("resolve user %d: %w", userID, err)
	}

	for _, userClass := range users {
		if user, ok := userClass.(*tg.User); ok && user.ID == userID {
			sender := mapUserSender(user)
			c.users[userID] = sender

			return sender, nil
		}
	}

	c.missed[userID] = true

	return filters.Sender{}, nil
}

func (c *SenderCache) resolveChannel(ctx context.Context, channelID int64) (filters.Sender, error) {
	if c.missed[channelID] {
		return filters.Sender{}, nil
	}

	result, err := c.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: channelID},
	})
	if err != nil {
		c.missed[channelID] = true

		return filters.Sender{}, fmt.Errorf("resolve channel %d: %w", channelID, err)
	}

	chats, ok := result.(*tg.MessagesChats)
	if !ok {
		c.missed[channelID] = true

		return filters.Sender{}, nil
	}

	for _, chatClass := range chats.Chats {
		if channel, valid := chatClass.(*tg.Channel); valid && channel.ID == channelID {
			sender := mapChannelSender(channel)
			c.channels[channelID] = sender

			return sender, nil
		}
	}

	c.missed[channelID] = true

	return filters.Sender{}, nil
}

func mapUserSender(user *tg.User) filters.Sender {
	return filters.Sender{
		Present:    true,
		ID:         user.ID,
		Name:       strings.TrimSpace(user.FirstName + " " + user.LastName),
		Username:   user.Username,
		Phone:      user.Phone,
		IsBot:      user.Bot,
		IsContact:  user.Contact,
		IsMutual:   user.MutualContact,
		IsPremium:  user.Premium,
		IsVerified: user.Verified,
		IsScam:     user.Scam,
		IsDeleted:  user.Deleted,
	}
}

func mapChannelSender(channel *tg.Channel) filters.Sender {
	return filters.Sender{
		Present:  true,
		ID:       channel.ID,
		Name:     channel.Title,
		Username: channel.Username,
	}
}
