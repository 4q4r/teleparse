package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	tgclient "github.com/4q4r/teleparse/internal/tg"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	tgapi "github.com/gotd/td/tg"
	"github.com/spf13/cobra"
)

// Explicit-id fetch tuning: ids per messages.getMessages call and the album
// sibling window span (Telegram albums hold at most ten items).
const (
	getChunkSize = 100
	albumSpan    = 9
)

// errGetSource marks get-command input mistakes (no links, conflicting
// sources).
var errGetSource = errors.New("nothing to fetch")

// getAPI bundles the surfaces explicit-id fetching needs: the refetch seam
// (messages/channels.getMessages), the topic feed seam (getReplies through
// the walk feed factory) and sender resolution.
type getAPI interface {
	refetchAPI
	scan.WalkAPI
	walkSenderAPI
}

// getJob carries the get-mode fetch plan through executeRun: either parsed
// message links or a desktop-export manifest, never both.
type getJob struct {
	links  []linkTarget
	group  bool
	export *exportJob
}

// peerFetch pairs one resolved target with the link contexts that belong to
// its chat.
type peerFetch struct {
	target   scan.Target
	contexts []linkTarget
}

// getFlags carries the get-command knobs beyond the dl-family set.
type getFlags struct {
	fromExport string
	chat       string
	mediaDir   string
	group      bool
}

func getCmd(app *App) *cobra.Command {
	var (
		filterSet filterFlags
		flags     dlRunFlags
		getOpts   getFlags
	)

	cmd := &cobra.Command{
		Use:   "get [LINKS]...",
		Short: "Fetch specific messages by t.me link, id list or range (downloads unless --dry-run)",
		Example: "  teleparse get https://t.me/tdl/100\n" +
			"  teleparse get t.me/news/100-200 --media photo\n" +
			"  teleparse get t.me/c/123456/789 --group=false\n" +
			"  teleparse get --from-export ~/ChatExport_2026-09-06/result.json",
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runGetCommand(app, c, args, &filterSet, flags, getOpts)
		},
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "list matches, download nothing (same as scan)")
	cmd.Flags().BoolVar(&flags.explain, "explain", false,
		"print which filters push down to the server vs run client-side, then proceed")
	cmd.Flags().BoolVar(&flags.countOnly, "count-only", false, "only print per-chat match counts")
	cmd.Flags().BoolVar(&flags.takeout, "takeout", false, "wrap session in takeout mode (lower flood limits)")
	cmd.Flags().BoolVar(&flags.noTakeout, "no-takeout", false, "never use takeout mode, even if auto would engage")
	cmd.Flags().BoolVar(&getOpts.group, "group", true,
		"expand albums: fetch the siblings of a grouped message (id±9, same grouped_id)")
	cmd.Flags().StringVar(&getOpts.fromExport, "from-export", "",
		"ingest a Telegram Desktop export result.json as a download manifest (adopts files already on disk)")
	cmd.Flags().StringVar(&getOpts.chat, "chat", "",
		"chat spec overriding the export's own chat identity (@user | link | id)")
	cmd.Flags().StringVar(&getOpts.mediaDir, "media-dir", "",
		"directory holding the export's media (default: the export file's directory)")
	cmd.Flags().String("profile", "", "named filter profile overlay")
	addNotifyWebhookFlag(cmd)
	addRewriteExtFlag(cmd)
	addSilentOutputMirror(cmd)
	addFilterFlags(cmd, &filterSet)

	return cmd
}

// runGetCommand validates the link/export inputs up front, then rides the
// shared download pipeline once per account.
func runGetCommand(
	app *App, cmd *cobra.Command, args []string, filterSet *filterFlags, flags dlRunFlags, getOpts getFlags,
) error {
	job, err := buildGetJob(args, getOpts)
	if err != nil {
		return fail(cmd, err)
	}

	opts, profileName, err := effectiveOptions(app.cfg, cmd, filterSet)
	if err != nil {
		return fail(cmd, err)
	}

	plan, err := filters.Compile(&opts)
	if err != nil {
		return fail(cmd, err)
	}

	if flags.explain {
		if err := printExplain(cmd, plan); err != nil {
			return fail(cmd, err)
		}
	}

	accounts, err := accountList(app, cmd)
	if err != nil {
		return fail(cmd, err)
	}

	creds, err := app.resolveCreds()
	if err != nil {
		return fail(cmd, err)
	}

	if err := runConnectivityCheck(cmd, app); err != nil {
		return fail(cmd, err)
	}

	if flags.takeout && flags.noTakeout {
		return fail(cmd, errTakeoutExclusive)
	}

	mode := runMode{dryRun: flags.dryRun, countOnly: flags.countOnly, getMode: true}

	for _, account := range accounts {
		cfg := *app.cfg
		cfg.Auth.Account = account

		runErr := tgclient.Run(cmd.Context(), account, creds, &cfg, app.paths, func(
			ctx context.Context,
			client *telegram.Client,
		) error {
			return runAccountSession(ctx, cmd, app, account, profileName, opts, plan, getJobSpecs(job),
				mode, client, &cfg, flags, nil, job)
		})
		if runErr != nil {
			return fail(cmd, runErr)
		}
	}

	return nil
}

// buildGetJob parses the link arguments or the desktop-export manifest into
// the fetch plan; the two sources are mutually exclusive.
func buildGetJob(args []string, getOpts getFlags) (*getJob, error) {
	job := &getJob{group: getOpts.group}

	switch {
	case getOpts.fromExport != "":
		if len(args) > 0 {
			return nil, fmt.Errorf("links and --from-export are mutually exclusive: %w", errGetSource)
		}

		parsed, err := parseDesktopExport(getOpts.fromExport, getOpts.mediaDir, getOpts.chat)
		if err != nil {
			return nil, err
		}

		job.export = parsed

		return job, nil
	case getOpts.mediaDir != "":
		return nil, fmt.Errorf("--media-dir needs --from-export: %w", errGetSource)
	case len(args) == 0:
		return nil, fmt.Errorf("pass one or more t.me links, or --from-export result.json: %w", errGetSource)
	default:
		links, err := parseLinks(args)
		if err != nil {
			return nil, err
		}

		job.links = links

		return job, nil
	}
}

// getJobSpecs lists the chat specs the job resolves, in first-seen order.
func getJobSpecs(job *getJob) []string {
	if job.export != nil {
		return []string{job.export.chatSpec}
	}

	specs := make([]string, 0, len(job.links))
	seen := map[string]bool{}

	for _, target := range job.links {
		if seen[target.PeerSpec] {
			continue
		}

		seen[target.PeerSpec] = true

		specs = append(specs, target.PeerSpec)
	}

	return specs
}

// linkContextsByChat pairs every link context with the resolved chat of its
// peer spec; specs and targets arrive in the same first-seen order.
func linkContextsByChat(specs []string, targets []scan.Target, contexts []linkTarget) map[int64][]linkTarget {
	specToChat := make(map[string]int64, len(specs))

	for idx := range min(len(specs), len(targets)) {
		specToChat[specs[idx]] = targets[idx].Chat.ID
	}

	byChat := map[int64][]linkTarget{}

	for _, context := range contexts {
		chatID, ok := specToChat[context.PeerSpec]
		if !ok {
			continue
		}

		byChat[chatID] = append(byChat[chatID], context)
	}

	return byChat
}

// fetchTargetMessages fetches the explicit message ids of every peer
// context, maps them through the shared walk projection, applies the
// compiled filters and feeds matches into the collector — the same emit
// contract the history walk uses.
func fetchTargetMessages(
	ctx context.Context, api getAPI, fetches []peerFetch,
	plan *filters.Plan, collector *walkCollector, group bool,
) error {
	senders := scan.NewSenderCache(api)

	for _, fetch := range fetches {
		if err := fetchPeerMessages(ctx, api, fetch, plan, collector, senders, group); err != nil {
			return err
		}
	}

	return nil
}

// fetchPeerMessages serves one target: flat contexts fetch their ids through
// the refetch seam, topic contexts ride the getReplies feed windowed to the
// wanted ids. Album expansion (group) fetches the same-grouped siblings of a
// grouped message once per album.
func fetchPeerMessages(
	ctx context.Context, api getAPI, fetch peerFetch,
	plan *filters.Plan, collector *walkCollector, senders *scan.SenderCache, group bool,
) error {
	fetched := map[int]bool{}
	expanded := map[int64]bool{}

	for _, context := range fetch.contexts {
		messages, err := fetchContextMessages(ctx, api, fetch.target, context, senders)
		if err != nil {
			return err
		}

		for _, msg := range messages {
			if err := processGetMessage(ctx, fetch.target, msg, plan, collector, senders); err != nil {
				return err
			}

			fetched[msg.ID] = true

			if err := expandAlbum(ctx, api, fetch.target, msg, plan, collector, senders, group, fetched, expanded); err != nil {
				return err
			}
		}
	}

	return nil
}

// fetchContextMessages fetches one link context's messages: getReplies for
// topic/comment contexts, explicit ids otherwise.
func fetchContextMessages(
	ctx context.Context, api getAPI, target scan.Target,
	context linkTarget, senders *scan.SenderCache,
) ([]*tgapi.Message, error) {
	if context.TopicID > 0 {
		return fetchTopicWindow(ctx, api, target, context, senders)
	}

	return fetchIDsFlat(ctx, api, target, context.MsgIDs, senders)
}

// fetchIDsFlat fetches explicit ids chunked through the refetch seam:
// channels.getMessages for channel peers, messages.getMessages otherwise.
func fetchIDsFlat(
	ctx context.Context, api getAPI, target scan.Target, ids []int, senders *scan.SenderCache,
) ([]*tgapi.Message, error) {
	messages := make([]*tgapi.Message, 0, len(ids))

	for _, chunk := range chunkIDs(ids, getChunkSize) {
		fetched, err := fetchByIDs(ctx, api, target, chunk, senders)
		if err != nil {
			return nil, err
		}

		messages = append(messages, fetched...)
	}

	return messages, nil
}

// fetchByIDs issues one explicit-id RPC for the peer and seeds the sender
// cache from the response entity set.
func fetchByIDs(
	ctx context.Context, api getAPI, target scan.Target, ids []int, senders *scan.SenderCache,
) ([]*tgapi.Message, error) {
	request := make([]tgapi.InputMessageClass, 0, len(ids))

	for _, id := range ids {
		request = append(request, &tgapi.InputMessageID{ID: id})
	}

	var (
		result tgapi.MessagesMessagesClass
		err    error
	)

	if channel, isChannel := target.InputPeer.(*tgapi.InputPeerChannel); isChannel {
		result, err = api.ChannelsGetMessages(ctx, &tgapi.ChannelsGetMessagesRequest{
			Channel: &tgapi.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      request,
		})
		if err != nil {
			return nil, fmt.Errorf("channels.getMessages of %q: %w", target.Chat.Title, err)
		}
	} else {
		result, err = api.MessagesGetMessages(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("messages.getMessages of %q: %w", target.Chat.Title, err)
		}
	}

	seedSenders(senders, result)

	return messagesOfClass(result), nil
}

// fetchTopicWindow walks the getReplies thread bounded to the wanted ids
// (min-1..max+1), collecting exactly the requested messages.
func fetchTopicWindow(
	ctx context.Context, api getAPI, target scan.Target, context linkTarget, senders *scan.SenderCache,
) ([]*tgapi.Message, error) {
	wanted := make(map[int]bool, len(context.MsgIDs))

	bounds := idBounds(context.MsgIDs)

	for _, id := range context.MsgIDs {
		wanted[id] = true
	}

	feed := scan.HistoryFeeds(api)(scan.FeedRequest{
		Peer: target.InputPeer, MsgID: context.TopicID,
		MinID: int64(bounds.min - 1), MaxID: int64(bounds.max + 1),
	})

	messages := make([]*tgapi.Message, 0, len(wanted))

	for feed.Next(ctx) {
		msg, ok := feed.Value().Msg.(*tgapi.Message)
		if !ok {
			continue
		}

		senders.Seed(feed.Value().Entities)

		if !wanted[msg.ID] {
			continue
		}

		messages = append(messages, msg)

		delete(wanted, msg.ID)

		if len(wanted) == 0 {
			return messages, nil
		}
	}

	if err := feed.Err(); err != nil {
		return nil, fmt.Errorf("fetch thread %d of %q: %w", context.TopicID, target.Chat.Title, err)
	}

	return messages, nil
}

// expandAlbum fetches the sibling window (id±albumSpan) of a grouped
// message once per album and processes the same-grouped members the window
// returns; already-fetched ids never re-fetch.
func expandAlbum(
	ctx context.Context, api getAPI, target scan.Target, msg *tgapi.Message,
	plan *filters.Plan, collector *walkCollector, senders *scan.SenderCache,
	group bool, fetched map[int]bool, expanded map[int64]bool,
) error {
	if !group || msg.GroupedID == 0 || expanded[msg.GroupedID] {
		return nil
	}

	expanded[msg.GroupedID] = true

	window := make([]int, 0, albumSpan*2)

	for id := max(msg.ID-albumSpan, 1); id <= msg.ID+albumSpan; id++ {
		if !fetched[id] {
			window = append(window, id)
		}
	}

	siblings, err := fetchByIDs(ctx, api, target, window, senders)
	if err != nil {
		return err
	}

	for _, sibling := range siblings {
		if sibling.GroupedID != msg.GroupedID || fetched[sibling.ID] {
			continue
		}

		if err := processGetMessage(ctx, target, sibling, plan, collector, senders); err != nil {
			return err
		}

		fetched[sibling.ID] = true
	}

	return nil
}

// processGetMessage projects one fetched message onto the filters model,
// applies the compiled predicates and feeds passing messages into the
// collector exactly like a walked match.
func processGetMessage(
	ctx context.Context, target scan.Target, msg *tgapi.Message,
	plan *filters.Plan, collector *walkCollector, senders *scan.SenderCache,
) error {
	mctx := scan.MapMessage(msg, target.Chat, senderForGet(ctx, senders, msg))

	if !planMatches(plan, &mctx) {
		return nil
	}

	collector.observe(mctx, msg)

	return nil
}

// senderForGet resolves the message author, degrading to an absent sender
// on failure like the walk does.
func senderForGet(ctx context.Context, senders *scan.SenderCache, msg *tgapi.Message) filters.Sender {
	fromID, ok := msg.GetFromID()
	if !ok {
		return filters.Sender{}
	}

	sender, err := senders.Get(ctx, fromID)
	if err != nil {
		return filters.Sender{}
	}

	return sender
}

// seedSenders fills the sender cache from a response's entity set when it
// carries one.
func seedSenders(senders *scan.SenderCache, result tgapi.MessagesMessagesClass) {
	if searchable, ok := result.(peer.EntitySearchResult); ok {
		senders.Seed(peer.EntitiesFromResult(searchable))
	}
}

// planMatches reports whether every compiled predicate accepts the context.
func planMatches(plan *filters.Plan, mctx *filters.Context) bool {
	for _, predicate := range plan.Predicates {
		if !predicate.Fn(mctx) {
			return false
		}
	}

	return true
}

// idBounds returns the smallest and largest id of a non-empty set.
type idRange struct {
	min int
	max int
}

func idBounds(ids []int) idRange {
	bounds := idRange{min: ids[0], max: ids[0]}

	for _, id := range ids[1:] {
		bounds.min = min(bounds.min, id)
		bounds.max = max(bounds.max, id)
	}

	return bounds
}

// chunkIDs splits ids into chunks of at most size.
func chunkIDs(ids []int, size int) [][]int {
	chunks := make([][]int, 0, (len(ids)+size-1)/size)

	for len(ids) > size {
		chunks = append(chunks, ids[:size])
		ids = ids[size:]
	}

	return append(chunks, ids)
}
