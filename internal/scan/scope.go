// Package scan bridges the gotd transport layer and the pure filters model:
// it resolves chat scopes, walks message history and projects gotd messages
// onto filters.Context for predicate evaluation.
package scan

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"teleparse/internal/filters"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query/dialogs"
	tg "github.com/gotd/td/tg"
)

// ErrUnknownChat marks unresolvable chat references.
var ErrUnknownChat = errors.New("unknown chat")

// Dialog batch size for the production lister.
const dialogsBatchSize = 100

// Target is one resolved walk destination: the input peer to fetch history
// from plus the context-shaped chat description.
type Target struct {
	InputPeer tg.InputPeerClass
	Chat      filters.Chat
}

// ScopeAPI is the raw resolution surface scope resolution needs; the
// *tg.Client returned by (*telegram.Client).API satisfies it.
type ScopeAPI interface {
	ContactsResolveUsername(
		ctx context.Context, request *tg.ContactsResolveUsernameRequest,
	) (*tg.ContactsResolvedPeer, error)

	ContactsGetContacts(ctx context.Context, hash int64) (tg.ContactsContactsClass, error)
}

// DialogLister enumerates every dialog visible to the account, together with
// each dialog's entity set.
type DialogLister interface {
	Each(ctx context.Context, fn func(dialog *tg.Dialog, entities peer.Entities) error) error
}

// ScopeDeps bundles the Telegram surfaces scope resolution needs.
type ScopeDeps struct {
	API     ScopeAPI
	Dialogs DialogLister
}

// ScopeResolver resolves chat scope specifications into walk targets.
type ScopeResolver struct {
	deps ScopeDeps
}

// NewScopeResolver returns a resolver over the given dependencies.
func NewScopeResolver(deps ScopeDeps) *ScopeResolver {
	return &ScopeResolver{deps: deps}
}

// Resolve turns scope specs into deduplicated targets. Specs are "all" (every
// dialog, prefiltered by opts), "saved" (Saved Messages), a @username, a
// t.me link or a numeric id resolved through the dialog cache. opts.SavedOnly
// short-circuits to the Saved Messages target.
func (r *ScopeResolver) Resolve(ctx context.Context, specs []string, opts filters.Options) ([]Target, error) {
	if opts.SavedOnly {
		return []Target{savedTarget()}, nil
	}

	targets := make([]Target, 0, len(specs))
	seen := make(map[int64]bool)

	for _, spec := range specs {
		added, err := r.resolveSpec(ctx, spec, opts, seen)
		if err != nil {
			return nil, err
		}

		targets = append(targets, added...)
	}

	return targets, nil
}

func (r *ScopeResolver) resolveSpec(
	ctx context.Context, spec string, opts filters.Options, seen map[int64]bool,
) ([]Target, error) {
	switch spec {
	case scopeAll:
		return r.resolveAll(ctx, opts, seen)
	case scopeSaved:
		return dedupe(seen, savedTarget()), nil
	default:
		return r.resolveNamed(ctx, spec, seen)
	}
}

func (r *ScopeResolver) resolveAll(ctx context.Context, opts filters.Options, seen map[int64]bool) ([]Target, error) {
	prefilter, err := newChatPrefilter(opts)
	if err != nil {
		return nil, err
	}

	var contacts map[int64]bool

	if opts.ContactsOnly {
		contacts, err = r.contactIDs(ctx)
		if err != nil {
			return nil, err
		}
	}

	targets := make([]Target, 0)

	err = r.deps.Dialogs.Each(ctx, func(dialog *tg.Dialog, entities peer.Entities) error {
		chat, ok := MapDialogChat(dialog, entities)
		if !ok {
			return nil
		}

		if !prefilter.allows(chat, contacts) {
			return nil
		}

		inputPeer, ok := dialogInputPeer(dialog, entities)
		if !ok {
			return nil
		}

		targets = append(targets, dedupe(seen, Target{InputPeer: inputPeer, Chat: chat})...)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerate dialogs: %w", err)
	}

	return targets, nil
}

func (r *ScopeResolver) resolveNamed(ctx context.Context, spec string, seen map[int64]bool) ([]Target, error) {
	if id, ok := numericSpec(spec); ok {
		return r.resolveByID(ctx, id, seen)
	}

	username, err := usernameOf(spec)
	if err != nil {
		return nil, err
	}

	resolved, err := r.deps.API.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: username,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w: %w", spec, ErrUnknownChat, err)
	}

	entities := peer.EntitiesFromResult(resolved)
	dialog := &tg.Dialog{Peer: resolved.Peer}

	chat, ok := MapDialogChat(dialog, entities)
	if !ok {
		return nil, fmt.Errorf("%q: %w", spec, ErrUnknownChat)
	}

	inputPeer, ok := dialogInputPeer(dialog, entities)
	if !ok {
		return nil, fmt.Errorf("%q: %w", spec, ErrUnknownChat)
	}

	return dedupe(seen, Target{InputPeer: inputPeer, Chat: chat}), nil
}

func (r *ScopeResolver) resolveByID(ctx context.Context, id int64, seen map[int64]bool) ([]Target, error) {
	found := make([]Target, 0, 1)

	err := r.deps.Dialogs.Each(ctx, func(dialog *tg.Dialog, entities peer.Entities) error {
		if peerID(dialog.Peer) != id {
			return nil
		}

		chat, ok := MapDialogChat(dialog, entities)
		if !ok {
			return nil
		}

		inputPeer, ok := dialogInputPeer(dialog, entities)
		if !ok {
			return nil
		}

		found = append(found, Target{InputPeer: inputPeer, Chat: chat})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan dialogs for id %d: %w", id, err)
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("id %d: %w (run `teleparse chats list` first to warm the dialog cache)", id, ErrUnknownChat)
	}

	return dedupe(seen, found[0]), nil
}

func (r *ScopeResolver) contactIDs(ctx context.Context) (map[int64]bool, error) {
	result, err := r.deps.API.ContactsGetContacts(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}

	list, ok := result.(*tg.ContactsContacts)
	if !ok {
		return map[int64]bool{}, nil
	}

	ids := make(map[int64]bool, len(list.Users))

	for _, userClass := range list.Users {
		if user, valid := userClass.(*tg.User); valid {
			ids[user.ID] = true
		}
	}

	return ids, nil
}

// chatPrefilter holds the client-side chat constraints applied while
// expanding the "all" spec.
type chatPrefilter struct {
	chatType        []string
	excludeChatType []string
	chatDeleted     filters.TriBool
	archived        string
	glob            string
	regex           *regexp.Regexp
	username        string
}

func newChatPrefilter(opts filters.Options) (chatPrefilter, error) {
	prefilter := chatPrefilter{
		chatType:        opts.ChatType,
		excludeChatType: opts.ExcludeChatType,
		chatDeleted:     opts.ChatDeleted,
		archived:        opts.Archived,
		glob:            opts.ChatGlob,
		username:        strings.TrimPrefix(strings.ToLower(opts.ChatUsername), "@"),
	}

	if opts.ChatRegex != "" {
		compiled, err := regexp.Compile(opts.ChatRegex)
		if err != nil {
			return prefilter, fmt.Errorf("chat_regex %q: %w", opts.ChatRegex, err)
		}

		prefilter.regex = compiled
	}

	return prefilter, nil
}

// excluded reports whether the exclude filters drop this chat: a listed
// exclude-chat-type or a chat-deleted tri mismatch.
func (p chatPrefilter) excluded(chat filters.Chat) bool {
	if len(p.excludeChatType) > 0 && containsString(p.excludeChatType, chat.Type) {
		return true
	}

	return p.chatDeleted.IsSet() && chat.Deleted != p.chatDeleted.Value()
}

func (p chatPrefilter) allows(chat filters.Chat, contacts map[int64]bool) bool {
	switch {
	case len(p.chatType) > 0 && !containsString(p.chatType, chat.Type):
		return false
	case p.excluded(chat):
		return false
	case p.archived == "only" && !chat.Archived:
		return false
	case p.archived == "exclude" && chat.Archived:
		return false
	case p.glob != "" && !globMatches(p.glob, chat.Title):
		return false
	case p.regex != nil && !p.regex.MatchString(chat.Title):
		return false
	case p.username != "" && strings.TrimPrefix(strings.ToLower(chat.Username), "@") != p.username:
		return false
	case contacts != nil && (chat.Type != chatPrivate || !contacts[chat.ID]):
		return false
	default:
		return true
	}
}

func globMatches(pattern, value string) bool {
	matched, err := path.Match(pattern, value)

	return err == nil && matched
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}

func savedTarget() Target {
	return Target{
		InputPeer: &tg.InputPeerSelf{},
		Chat:      filters.Chat{Type: chatPrivate, Title: "Saved Messages", Saved: true},
	}
}

// dedupe appends targets whose chat id has not been seen yet.
func dedupe(seen map[int64]bool, candidates ...Target) []Target {
	fresh := make([]Target, 0, len(candidates))

	for _, candidate := range candidates {
		if seen[candidate.Chat.ID] {
			continue
		}

		seen[candidate.Chat.ID] = true

		fresh = append(fresh, candidate)
	}

	return fresh
}

// numericSpec parses bare and -100-prefixed channel ids plus t.me/c/ links.
func numericSpec(spec string) (int64, bool) {
	clean := strings.TrimPrefix(spec, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	clean = strings.TrimPrefix(clean, "t.me/c/")

	id, err := strconv.ParseInt(strings.TrimPrefix(clean, "-100"), 10, 64)
	if err != nil {
		return 0, false
	}

	return id, true
}

// usernameOf extracts the username from @name, bare name and t.me link forms.
func usernameOf(spec string) (string, error) {
	clean := strings.TrimSpace(spec)
	clean = strings.TrimPrefix(clean, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	clean = strings.TrimPrefix(clean, "t.me/")
	clean = strings.TrimPrefix(clean, "telegram.me/")
	clean = strings.TrimPrefix(clean, "@")

	if clean == "" || strings.Contains(clean, "/") {
		return "", fmt.Errorf("%q: %w", spec, ErrUnknownChat)
	}

	return clean, nil
}

// dialogInputPeer builds the access-hash-bearing input peer for a dialog.
//
//nolint:ireturn // three concrete peer variants share one interface result
func dialogInputPeer(dialog *tg.Dialog, entities peer.Entities) (tg.InputPeerClass, bool) {
	switch peerType := dialog.Peer.(type) {
	case *tg.PeerUser:
		user, ok := entities.User(peerType.UserID)
		if !ok {
			return nil, false
		}

		return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, true
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: peerType.ChatID}, true
	case *tg.PeerChannel:
		channel, ok := entities.Channel(peerType.ChannelID)
		if !ok {
			return nil, false
		}

		return &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, true
	default:
		return nil, false
	}
}

// Scope spec keywords.
const (
	scopeAll   = "all"
	scopeSaved = "saved"
)

type queryDialogs struct {
	api *tg.Client
}

func (q queryDialogs) Each(ctx context.Context, visit func(*tg.Dialog, peer.Entities) error) error {
	iterator := dialogs.NewQueryBuilder(q.api).GetDialogs().BatchSize(dialogsBatchSize).Iter()

	for iterator.Next(ctx) {
		elem := iterator.Value()

		dialog, ok := elem.Dialog.(*tg.Dialog)
		if !ok {
			continue
		}

		if err := visit(dialog, elem.Entities); err != nil {
			return fmt.Errorf("visit dialog: %w", err)
		}
	}

	if err := iterator.Err(); err != nil {
		return fmt.Errorf("iterate dialogs: %w", err)
	}

	return nil
}

// ResolveClient resolves scope specs against a live telegram client using the
// production dialog lister.
func ResolveClient(
	ctx context.Context, client *telegram.Client, specs []string, opts filters.Options,
) ([]Target, error) {
	resolver := NewScopeResolver(ScopeDeps{API: client.API(), Dialogs: queryDialogs{api: client.API()}})

	targets, err := resolver.Resolve(ctx, specs, opts)
	if err != nil {
		return nil, fmt.Errorf("resolve scope: %w", err)
	}

	return targets, nil
}
