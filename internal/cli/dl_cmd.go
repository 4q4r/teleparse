package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/notify"
	"github.com/4q4r/teleparse/internal/pace"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/takeout"
	tgapi "github.com/gotd/td/tg"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// runIDTimeFormat and runIDSaltBytes shape generated run ids.
const (
	runIDTimeFormat = "20060102-150405"
	runIDSaltBytes  = 4
)

// dlRunFlags carries the dl-command knobs beyond the filter surface.
type dlRunFlags struct {
	dryRun    bool
	explain   bool
	countOnly bool
	takeout   bool
	noTakeout bool
	full      bool
	noRouting bool
}

// runMode tunes the shared pipeline: dry-run previews, count-only output,
// sync-mode watermarks, incremental (watermark-cached) walks and get-mode
// explicit-id fetching (which never advances walk watermarks).
type runMode struct {
	dryRun      bool
	countOnly   bool
	syncMode    bool
	incremental bool
	fullWalk    bool
	getMode     bool
}

// runPayload is the TOML envelope persisted in runs.filter_json: the chat
// specs plus the effective filter options, squashed flat.
type runPayload struct {
	Chats []string `toml:"chats"`
	teleparseOptionsEmbed
}

// teleparseOptionsEmbed squashes filters.Options into runPayload; named to
// keep the go-toml squash tag on an exported field.
type teleparseOptionsEmbed struct {
	Options filters.Options `toml:",squash"`
}

// addFullWalkFlag registers the shared --full opt-out of incremental walks.
func addFullWalkFlag(cmd *cobra.Command, flags *dlRunFlags) {
	cmd.Flags().BoolVar(&flags.full, "full", false,
		"force a full re-walk of every chat (ignore cached watermarks and rewalk freshness; overrides scan.incremental)")
}

// addNoRoutingFlag registers the shared --no-routing bypass of the
// [accounts] routing table for one invocation.
func addNoRoutingFlag(cmd *cobra.Command, flags *dlRunFlags) {
	cmd.Flags().BoolVar(&flags.noRouting, "no-routing", false,
		"process every chat on the default account, ignoring accounts.routing for this run")
}

// incrementalMode resolves whether this run walks incrementally: config
// default (on) unless --full forces a complete pass.
func incrementalMode(cfg *config.Config, flags dlRunFlags) bool {
	return cfg.Scan.Incremental && !flags.full
}

func dlCmd(app *App) *cobra.Command {
	var (
		filterSet filterFlags
		flags     dlRunFlags
	)

	cmd := &cobra.Command{
		Use:   "dl [CHATS]...",
		Short: "Download media matching filters (CHATS: all | @user | link | id | saved | glob)",
		Example: "  teleparse dl @durov --media photo --min-size 1MB\n" +
			"  teleparse dl all --chat-type channel --last 7d --dry-run\n" +
			"  teleparse dl saved --takeout",
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runDownloadCommand(app, c, args, &filterSet, flags, runMode{
				dryRun:      flags.dryRun,
				countOnly:   flags.countOnly,
				incremental: incrementalMode(app.cfg, flags),
				fullWalk:    flags.full,
			}, "", nil)
		},
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "list matches, download nothing (same as scan)")
	cmd.Flags().BoolVar(&flags.explain, "explain", false,
		"print which filters push down to the server vs run client-side, then proceed")
	cmd.Flags().BoolVar(&flags.countOnly, "count-only", false, "only print per-chat match counts")
	cmd.Flags().BoolVar(&flags.takeout, "takeout", false, "wrap session in takeout mode (lower flood limits)")
	cmd.Flags().BoolVar(&flags.noTakeout, "no-takeout", false, "never use takeout mode, even if auto would engage")
	addFullWalkFlag(cmd, &flags)
	addNoRoutingFlag(cmd, &flags)
	addNotifyWebhookFlag(cmd)
	addRewriteExtFlag(cmd)
	cmd.Flags().String("profile", "", "named filter profile overlay")
	addSilentOutputMirror(cmd)
	addFilterFlags(cmd, &filterSet)

	return cmd
}

// addSilentOutputMirror registers the dl-family -s shorthand for output
// silence; the plain --silent long name belongs to the silently-sent
// message filter registered by addFilterFlags.
func addSilentOutputMirror(cmd *cobra.Command) {
	cmd.Flags().BoolP("silent-output", "s", false,
		"suppress progress UI, per-item lines and summary (dl-family form of the\n"+
			"global -s; plain --silent here filters silently-sent messages)")
}

func scanCmd(app *App) *cobra.Command {
	var (
		filterSet filterFlags
		flags     dlRunFlags
	)

	cmd := &cobra.Command{
		Use:     "scan [CHATS]...",
		Short:   "Preview what dl would fetch (dry-run): plan + manifest rows, no downloads",
		Example: "  teleparse scan @durov --media video --count-only\n  teleparse scan all --chat-glob 'News*' --explain",
		Args:    cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runDownloadCommand(app, c, args, &filterSet, flags, runMode{
				dryRun:      true,
				incremental: incrementalMode(app.cfg, flags),
				fullWalk:    flags.full,
			}, "", nil)
		},
	}
	cmd.Flags().BoolVar(&flags.explain, "explain", false,
		"print which filters push down to the server vs run client-side, then proceed")
	cmd.Flags().BoolVar(&flags.countOnly, "count-only", false, "only print per-chat match counts")
	addFullWalkFlag(cmd, &flags)
	addNoRoutingFlag(cmd, &flags)
	addSilentOutputMirror(cmd)
	addFilterFlags(cmd, &filterSet)

	return cmd
}

// runDownloadCommand resolves effective options and accounts, then executes
// the pipeline once per account; accountOverride pins the account (resume);
// job non-nil switches the fetch phase to get-mode (nil for dl/scan).
func runDownloadCommand(app *App, cmd *cobra.Command, specs []string, filterSet *filterFlags,
	flags dlRunFlags, mode runMode, accountOverride string, job *getJob,
) error {
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

	accounts := []string{accountOverride}
	if accountOverride == "" {
		accounts, err = accountList(app, cmd)
		if err != nil {
			return fail(cmd, err)
		}
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

	// Routing tables and premium preference resolve (and validate) before
	// the first session opens, so a bad table fails the invocation at
	// start instead of mid-run. A pinned accountOverride (like --account)
	// skips routing entirely.
	var extras *invocationExtras

	if accountOverride == "" {
		extras, err = planInvocation(cmd.Context(), app, cmd, flags, job)
		if err != nil {
			return fail(cmd, err)
		}
	}

	for _, account := range accounts {
		cfg := *app.cfg
		cfg.Auth.Account = account

		runErr := accountSessionRunner(cmd.Context(), account, creds, &cfg, app.paths, func(
			ctx context.Context,
			client *telegram.Client,
		) error {
			return runAccountSession(ctx, cmd, app, account, profileName, opts, plan, specs, mode,
				client, &cfg, flags, extras, job)
		})
		if runErr != nil {
			return fail(cmd, runErr)
		}
	}

	return runDeferredSessions(cmd, app, creds, extras, profileName, opts, plan, mode, flags)
}

// runAccountSession wraps one connected client session: takeout decision,
// engagement notice and the fail-soft fallback to the plain API when an
// AUTO-engaged takeout session cannot start. extras carries the routing /
// premium-deferral plan (nil on resume, get and deferred passes).
func runAccountSession(
	ctx context.Context,
	cmd *cobra.Command,
	app *App,
	account, profileName string,
	opts filters.Options,
	plan *filters.Plan,
	specs []string,
	mode runMode,
	client *telegram.Client,
	cfg *config.Config,
	flags dlRunFlags,
	extras *invocationExtras,
	job *getJob,
) error {
	{
		takeoutMode, reason := resolveTakeoutModeFor(flags, cfg, specs,
			func() (int, error) { return tg.DialogCount(ctx, client) })

		// Cache-only premium resolution (no RPC before the session opens):
		// an unknown answer defaults the export cap to the base 2GiB, and
		// the cache-populating query later in downloadRun lets following
		// runs pick up the premium 4GiB cap.
		premium := tg.NewAccountManager(app.paths.AccountsDir).
			AccountPremium(ctx, account, nil)

		if takeoutMode && !app.silentMode(cmd) {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Dim("takeout: engaged ("+reason+
				") - export rate limits apply; the export session finishes when the run ends")); err != nil {
				return fmt.Errorf("print takeout notice: %w", err)
			}

			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Dim(takeoutCapNotice(premium))); err != nil {
				return fmt.Errorf("print takeout cap notice: %w", err)
			}
		}

		runAPI := func(takeoutEnabled bool) error {
			return withAPI(ctx, client, takeoutEnabled, premium.Premium, func(
				ctx context.Context,
				api *tgapi.Client,
			) error {
				return executeRun(ctx, cmd, app, account, profileName, opts, plan, specs, mode, api, client,
					takeoutEnabled, takeoutFileCap(takeoutEnabled, premium.Premium), extras, job)
			}, func(finishErr error) {
				if app.silentMode(cmd) {
					return
				}

				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Warning(
					"takeout session finished with a server warning (ignored): "+finishErr.Error()))
			})
		}

		runErr := runAPI(takeoutMode)
		if retry, notice := takeoutFallback(!takeoutMode, reason, runErr); retry {
			if !app.silentMode(cmd) {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Warning(notice)); err != nil {
					return fmt.Errorf("print takeout fallback notice: %w", err)
				}
			}

			return runAPI(false)
		}

		if runErr != nil && isTakeoutInitFailure(runErr) {
			return fmt.Errorf("%w: %w", ErrTakeoutInitUnavailable, runErr)
		}

		return runErr
	}
}

// withAPI runs fn with the raw API client, wrapped in a takeout session
// when requested so downloads ride the export rate-limit path.
// errTakeoutCallbackNotRun marks that the takeout session failed BEFORE
// the wrapped run started (init phase); distinguishing it from a nil
// callback error lets finish-phase noise be downgraded to a warning.
var errTakeoutCallbackNotRun = errors.New("takeout init failed before the run started")

// withAPI runs fn with the raw API client, wrapped in a takeout session
// when requested so downloads ride the export rate-limit path; premium
// selects the session's FileMaxSize cap. Finish-phase failures (e.g.
// TAKEOUT_REQUIRED after a long run) never fail an otherwise-successful
// run: the abandoned session simply expires server-side; onFinishWarn
// (nil-safe) receives the reason.
func withAPI(
	ctx context.Context,
	client *telegram.Client,
	takeoutMode bool,
	premium bool,
	runAPI func(context.Context, *tgapi.Client) error,
	onFinishWarn func(error),
) error {
	if !takeoutMode {
		return runAPI(ctx, client.API())
	}

	callbackErr := errTakeoutCallbackNotRun

	err := takeout.Run(ctx, client, takeoutConfigFor(premium), func(ctx context.Context, session *takeout.Client) error {
		callbackErr = runAPI(ctx, tgapi.NewClient(session))

		return callbackErr
	})
	if err == nil {
		return nil
	}

	// Init-phase failure: the callback never ran (auto-takeout fallback
	// and user errors depend on seeing this).
	if errors.Is(callbackErr, errTakeoutCallbackNotRun) {
		return fmt.Errorf("run takeout session: %w", err)
	}

	// Callback failure is the real result; finish noise is dropped.
	if callbackErr != nil {
		return callbackErr
	}

	// The run itself succeeded: only the finish call failed. Downgrade.
	if onFinishWarn != nil {
		onFinishWarn(err)
	}

	return nil
}

// executeRun walks the resolved scope, records the run, then previews or
// downloads everything the filters matched; decomposed into helpers below.
// takeoutCap is the active export session's file cap (zero off takeout);
// extras carries the routing / premium-deferral plan (nil disables both);
// job non-nil replaces the walk phase with get-mode explicit fetching.
func executeRun(ctx context.Context, cmd *cobra.Command, app *App, account, profileName string,
	opts filters.Options, plan *filters.Plan, specs []string, mode runMode,
	api *tgapi.Client, client *telegram.Client, takeoutActive bool, takeoutCap int64,
	extras *invocationExtras, job *getJob,
) error {
	if job != nil {
		specs = getJobSpecs(job)
	}

	state, err := openStore(app) //nolint:contextcheck // store.Open takes no context
	if err != nil {
		return err
	}

	defer func() { _ = state.Close() }()

	runID, err := newRunID()
	if err != nil {
		return err
	}

	payload, err := encodeRunPayload(specs, opts)
	if err != nil {
		return err
	}

	if err := state.CreateRun(ctx, &store.Run{
		RunID: runID, Account: account, Profile: stringOrNil(profileName), FilterJSON: payload,
	}); err != nil {
		return fmt.Errorf("create run: %w", err)
	}

	targets, err := scan.ResolveClient(ctx, client, specs, opts)
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	// Per-chat account routing (walk runs only): matched chats leave this
	// pass for a routed session opened after this one finishes.
	targets, err = splitRoutedTargets(cmd, app, extras, job, targets)
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	minAge, err := scan.MinAgeFromOptions(opts)
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	if minAge > 0 {
		targets, err = filterMinAgeWithProgress(ctx, cmd, app, api, targets, minAge, opts.ChatMinAge)
		if err != nil {
			return finishRunE(ctx, state, runID, err)
		}
	}

	resolver, collector := newRunResolver(app, api, state, rewriteExtMode(app.cfg, cmd))

	rewalkMinAge, err := app.cfg.Scan.RewalkAge()
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	// Every walk mode — plain dl and sync as much as dry-run and
	// count-only — renders the live walk progress instead of minutes of
	// silence before downloads appear.
	progress := walkProgressFor(mode, cmd, app, len(targets))

	walkStarted := time.Now()

	// Get-mode link contexts, bucketed per resolved chat before the loop so
	// each iteration fetches exactly its own target's ids.
	var linkContexts map[int64][]linkTarget

	if job != nil && job.export == nil {
		linkContexts = linkContextsByChat(specs, targets, job.links)
	}

	for _, target := range targets {
		if err := executeTarget(ctx, cmd, app, state, api, resolver, collector, job,
			plan, opts, mode, progress, rewalkMinAge, linkContexts, target); err != nil {
			progress.close()

			return finishRunE(ctx, state, runID, err)
		}
	}

	progress.close()

	if mode.dryRun || mode.countOnly {
		return previewRun(ctx, cmd, app, state, runID, collector, targets, mode, time.Since(walkStarted))
	}

	// The walk surface is closed above; the transition line hands the
	// terminal over to the download reporter that downloadRun opens.
	if err := printWalkTransition(cmd, app, len(targets), len(collector.items), cachedTotal(collector)); err != nil {
		return err
	}

	return downloadRun(ctx, cmd, state, app, runID, account, resolver,
		collector, targets, opts, api, client, takeoutActive, takeoutCap, extras, mode)
}

// executeTarget runs one target's fetch phase — a history walk for dl-family
// runs, explicit-id fetching or export adoption for get jobs — and settles
// the progress line with the matches it produced.
func executeTarget(
	ctx context.Context,
	cmd *cobra.Command,
	app *App,
	state *store.Store,
	api *tgapi.Client,
	resolver *runResolver,
	collector *walkCollector,
	job *getJob,
	plan *filters.Plan,
	opts filters.Options,
	mode runMode,
	progress *scanProgress,
	rewalkMinAge time.Duration,
	linkContexts map[int64][]linkTarget,
	target scan.Target,
) error {
	if err := state.UpsertChat(ctx, chatFromTarget(target)); err != nil {
		return fmt.Errorf("upsert chat %d: %w", target.Chat.ID, err)
	}

	resolver.peers[target.Chat.ID] = target.InputPeer

	before, began := len(collector.items), time.Now()

	progress.chatStart(chatLabel(target.Chat.ID, target.Chat.Title))

	switch {
	case job != nil && job.export != nil:
		if err := adoptExportRows(ctx, cmd, app, state, resolver, collector, job.export, plan, mode, target); err != nil {
			return err
		}
	case job != nil:
		fetch := []peerFetch{{target: target, contexts: linkContexts[target.Chat.ID]}}

		if err := fetchTargetMessages(ctx, api, fetch, plan, collector, job.group); err != nil {
			return err
		}
	default:
		if err := walkTarget(ctx, state, api, target, plan, opts, mode, collector, progress, rewalkMinAge); err != nil {
			return err
		}
	}

	// The settled line shows new+cached: incremental walks carry the
	// prior-run manifest count resolveWalkWindow stashed, so a chat
	// whose matches all came from earlier runs never renders (0).
	progress.chatDone(chatLabel(target.Chat.ID, target.Chat.Title),
		len(collector.items)-before, int(collector.cached[target.Chat.ID]), time.Since(began))

	return nil
}

// walkCollector accumulates manifest items, walk context and per-chat
// message-id highs while the walk runs. cached stashes each incrementally
// walked chat's prior-run manifest count before new rows are persisted, and
// incremental records which chats were walked from their watermark.
type walkCollector struct {
	cache       *messageCache
	items       []store.MediaItem
	maxSeen     map[int64]int64
	cached      map[int64]int64
	incremental map[int64]bool
}

// walkSenderAPI is the sender-resolution surface the walk needs beyond
// history pages; it mirrors scan's unexported SenderAPI so the walkTarget
// seam accepts one interface.
type walkSenderAPI interface {
	UsersGetUsers(ctx context.Context, id []tgapi.InputUserClass) ([]tgapi.UserClass, error)
	ChannelsGetChannels(ctx context.Context, id []tgapi.InputChannelClass) (tgapi.MessagesChatsClass, error)
}

// walkAPI bundles history iteration and sender resolution for walkTarget.
type walkAPI interface {
	scan.WalkAPI
	walkSenderAPI
}

func walkTarget(ctx context.Context, state *store.Store, api walkAPI, target scan.Target,
	plan *filters.Plan, opts filters.Options, mode runMode, collector *walkCollector,
	progress *scanProgress, rewalkMinAge time.Duration,
) error {
	walkOpts, watermark, freshSkip, err := resolveWalkWindow(ctx, state, api, target, opts, mode,
		collector, progress, rewalkMinAge)
	if err != nil {
		return err
	}

	if watermark > 0 {
		collector.incremental[target.Chat.ID] = true
	}

	// A freshly walked chat needs no pages at all: the run proceeds on the
	// cached manifest and the caller settles the progress line with the
	// stashed cached count.
	if freshSkip {
		return nil
	}

	emit := func(fctx filters.Context, msg *tgapi.Message) error {
		collector.observe(fctx, msg)

		return nil
	}

	walker := scan.NewHistoryWalker(api, scan.HistoryFeeds(api), scan.NewSenderCache(api))

	if err := walker.Walk(ctx, target, plan, walkOpts, emit); err != nil {
		return fmt.Errorf("walk %q: %w", chatLabel(target.Chat.ID, target.Chat.Title), err)
	}

	return nil
}

// resolveWalkWindow decides how much history this target needs: sync mode
// and incremental runs with a stored watermark walk only past it; a newest
// message id below the watermark means the chat was mass-cleared, so the
// watermark resets and the walk covers full history again. A chat whose
// last walk is younger than rewalkMinAge skips the walk entirely (reported
// as freshSkip; the cached manifest serves the run). The returned watermark
// is the injected MinID (zero for a full walk).
func resolveWalkWindow(
	ctx context.Context,
	state *store.Store,
	api scan.WalkAPI,
	target scan.Target,
	opts filters.Options,
	mode runMode,
	collector *walkCollector,
	progress *scanProgress,
	rewalkMinAge time.Duration,
) (filters.Options, int64, bool, error) {
	walkOpts := opts

	if mode.fullWalk || (!mode.syncMode && !mode.incremental) {
		return walkOpts, 0, false, nil
	}

	watermark, err := state.Watermark(ctx, target.Chat.ID)
	if err != nil {
		return filters.Options{}, 0, false, fmt.Errorf("read watermark chat %d: %w", target.Chat.ID, err)
	}

	if watermark == 0 {
		return walkOpts, 0, false, nil
	}

	freshSkip, err := chatFreshlyWalked(ctx, state, target, collector, rewalkMinAge)
	if err != nil {
		return filters.Options{}, 0, false, err
	}

	if freshSkip {
		return walkOpts, watermark, true, nil
	}

	newest := target.NewestID
	if newest == 0 {
		newest, err = scan.NewestMessageID(ctx, api, target.InputPeer)
		if err != nil {
			return filters.Options{}, 0, false, fmt.Errorf("probe newest message of %q: %w", target.Chat.Title, err)
		}
	}

	if scan.HistoryCleared(watermark, newest) {
		if err := state.ResetWatermark(ctx, target.Chat.ID); err != nil {
			return filters.Options{}, 0, false, fmt.Errorf("reset watermark chat %d: %w", target.Chat.ID, err)
		}

		progress.chatCleared(target.Chat.Title)

		return walkOpts, 0, false, nil
	}

	if !scan.ShouldWalkIncrementally(watermark, newest) {
		return walkOpts, 0, false, nil
	}

	walkOpts.MinID = watermark
	walkOpts.Reverse = true

	cached, err := state.CachedMatched(ctx, target.Chat.ID)
	if err != nil {
		return filters.Options{}, 0, false, fmt.Errorf("count cached matches chat %d: %w", target.Chat.ID, err)
	}

	collector.cached[target.Chat.ID] = int64(cached)

	return walkOpts, watermark, false, nil
}

// chatFreshlyWalked reports whether the chat's last completed walk is
// younger than rewalkMinAge, in which case the run skips the walk entirely.
// The cached manifest count is stashed on the collector so the settled
// progress line and download totals stay honest without a single page fetch.
func chatFreshlyWalked(
	ctx context.Context,
	state *store.Store,
	target scan.Target,
	collector *walkCollector,
	rewalkMinAge time.Duration,
) (bool, error) {
	if rewalkMinAge <= 0 {
		return false, nil
	}

	lastWalked, walked, err := state.ChatLastWalked(ctx, target.Chat.ID)
	if err != nil {
		return false, fmt.Errorf("read last walk chat %d: %w", target.Chat.ID, err)
	}

	if !walked || time.Since(lastWalked) >= rewalkMinAge {
		return false, nil
	}

	cached, err := state.CachedMatched(ctx, target.Chat.ID)
	if err != nil {
		return false, fmt.Errorf("count cached matches chat %d: %w", target.Chat.ID, err)
	}

	collector.cached[target.Chat.ID] = int64(cached)

	return true, nil
}

func (c *walkCollector) observe(fctx filters.Context, msg *tgapi.Message) {
	if fctx.Message.ID > c.maxSeen[fctx.Chat.ID] {
		c.maxSeen[fctx.Chat.ID] = fctx.Message.ID
	}

	if msg == nil {
		return
	}

	c.cache.put(msgCacheKey{chatID: fctx.Chat.ID, msgID: fctx.Message.ID}, walkedMessage{fctx: &fctx, msg: msg})

	item, ok := mediaItemFromMessage(fctx, msg)
	if !ok {
		return
	}

	c.items = append(c.items, item)
}

func newRunResolver(app *App, api refetchAPI, state *store.Store, rewriteExt bool) (*runResolver, *walkCollector) {
	cache := newMessageCache(msgCacheLimit)

	resolver := &runResolver{
		root:       app.paths.Downloads,
		template:   app.cfg.Output.Template,
		naming:     app.cfg.Output.Naming,
		rewriteExt: rewriteExt,
		cache:      cache,
		peers:      map[int64]tgapi.InputPeerClass{},
		api:        api,
		titles:     state,
		titleMemo:  map[int64]string{},
	}

	collector := &walkCollector{
		cache:       cache,
		maxSeen:     map[int64]int64{},
		cached:      map[int64]int64{},
		incremental: map[int64]bool{},
	}

	return resolver, collector
}

func previewRun(ctx context.Context, cmd *cobra.Command, app *App, state *store.Store, runID string,
	collector *walkCollector, targets []scan.Target, mode runMode, walkTime time.Duration,
) error {
	duplicates := 0

	for idx := range collector.items {
		item := collector.items[idx]
		item.Status = store.StatusDiscovered

		stored, err := state.UpsertMedia(ctx, &item)
		if err != nil {
			return finishRunE(ctx, state, runID, err)
		}

		if !stored {
			duplicates++
		}
	}

	if mode.countOnly {
		if err := printCounts(cmd, app, collector, targets, duplicates); err != nil {
			return err
		}
	} else if err := printPlan(cmd, collector); err != nil {
		return err
	}

	if err := printScanSummary(cmd, app, len(targets), collector, duplicates, walkTime); err != nil {
		return err
	}

	// Preview walks carry no download result; every walked chat advanced
	// cleanly, so the cache builds from the very first run. Get-mode runs
	// fetch explicit ids, not a contiguous walk window, so they leave the
	// watermarks alone: an incremental walk must still cover everything
	// between zero and the newest message.
	if !mode.getMode {
		if err := advanceWalkedWatermarks(ctx, state, collector, targets); err != nil {
			return err
		}
	}

	if err := state.FinishRun(ctx, runID, store.StatusDone, ""); err != nil {
		return fmt.Errorf("finish run %s: %w", runID, err)
	}

	return nil
}

func downloadRun(ctx context.Context, cmd *cobra.Command, state *store.Store, app *App, runID string,
	account string, resolver *runResolver, collector *walkCollector, targets []scan.Target,
	opts filters.Options, api *tgapi.Client, client *telegram.Client, takeoutActive bool, takeoutCap int64,
	extras *invocationExtras, mode runMode,
) error {
	pacer := pace.New(pace.Config{
		Concurrency:         app.cfg.Pacing.Concurrency,
		DelayMin:            secondsDuration(app.cfg.Pacing.DelayMin),
		DelayMax:            secondsDuration(app.cfg.Pacing.DelayMax),
		FloodSleepThreshold: secondsDuration(float64(app.cfg.Pacing.FloodSleepThreshold)),
	})

	silent := app.silentMode(cmd)

	reporter, closeUI := newDownloadReporter(cmd, silent, app.noASCII)

	defer closeUI()

	// Premium autodetect: cache-first account lookup that never blocks
	// downloads; the boost only retunes sizing left at the defaults.
	premium := tg.NewAccountManager(app.paths.AccountsDir).
		AccountPremium(ctx, account, client.Self)

	_, connections := app.cfg.Download.Effective(premium.Premium)

	if !silent && connections != app.cfg.Download.Connections {
		if err := printLine(cmd, "premium boost: connections %d (source: %s)\n",
			connections, premium.Source); err != nil {
			return err
		}
	}

	// Ranged-engine wiring: per-DC media pools with a home-DC fallback
	// resolve each item's RPC target; pool failures degrade to the single
	// primary connection. Under an ACTIVE takeout session upload.getFile
	// outside the session fails instantly with 403 TAKEOUT_REQUIRED, so
	// downloads must ride the takeout invoker (nil pools). Both paths ride
	// the ranged engine: transfers resume at the exact .part offset.
	var pools download.InvokerSource

	if takeoutActive {
		if err := printTakeoutPoolsNotice(cmd, app, silent); err != nil {
			return err
		}
	} else {
		poolSet := tg.NewDownloadPools(client, int64(connections))
		defer func() { _ = poolSet.Close() }()

		pools = poolSet
	}

	mgr := download.NewManager(state, pacer, download.Config{
		Output:       app.cfg.Output,
		Hooks:        app.cfg.Hooks,
		Concurrency:  app.cfg.Pacing.Concurrency,
		RetryMax:     app.cfg.Pacing.RetryMax,
		Dedupe:       opts.Dedupe,
		SkipExisting: opts.SkipExisting,
		Root:         app.paths.Downloads,
		FileMaxSize:  takeoutCap,
	}, resolver, reporter)

	mgr.Fetch = download.FetchFor(pools, api, download.FetchOptions{
		Reporter: reporter,
	})

	// Incremental runs owe downloads not only for freshly walked matches
	// but for manifest rows earlier runs recorded without finishing them.
	items := collector.items

	if len(collector.incremental) > 0 {
		merged, err := withCachedPending(ctx, state, targets, collector)
		if err != nil {
			return finishRunE(ctx, state, runID, err)
		}

		items = merged
	}

	// Premium-preferred fallback: items this session cannot transfer but a
	// premium 4GiB session can leave the queue for the premium pass instead
	// of failing; without a local premium account they stay and fail with
	// the cap reason.
	items, deferErr := deferOversizedToPremium(cmd, app, extras, premium.Premium, takeoutCap, items)
	if deferErr != nil {
		return finishRunE(ctx, state, runID, deferErr)
	}

	if err := mgr.Enqueue(ctx, items); err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	reporter.SetPhase("downloading")

	started := time.Now()

	res, err := mgr.Run(ctx, runID)
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	if res.Parked {
		postRunWebhook(ctx, cmd, app, notify.EventParked, runID, account,
			len(targets), len(items), res, time.Since(started))

		return printLine(cmd, "parked: flood wait until %s — resume with `teleparse resume %s`\n",
			res.ResumeAt.Format(time.RFC3339), runID)
	}

	if err := finishStatus(ctx, state, runID, res); err != nil {
		return err
	}

	postRunWebhook(ctx, cmd, app, notify.EventDone, runID, account,
		len(targets), len(items), res, time.Since(started))

	// The live UI prints its own final summary on close; quiet mode keeps
	// the stdout one-liner; silent prints neither.
	_, live := reporter.(*download.LiveReporter)

	if !silent && !live {
		if err := printSummary(cmd, res, time.Since(started)); err != nil {
			return err
		}
	}

	// Every completed download run refreshes the cache: full walks walked
	// everything, incremental walks everything past the watermark. Get-mode
	// runs never advance watermarks — they fetched explicit ids, not a
	// contiguous window.
	if mode.getMode {
		return nil
	}

	return advanceWatermarks(ctx, cmd, state, res, collector, targets, silent)
}

// walkTransition renders the walk-to-download handover text; the cached
// segment appears only when incremental walks carried prior-run matches.
func walkTransition(chats, matched int, cached int64) string {
	if cached > 0 {
		return fmt.Sprintf("walked %d chats, matched %d files (+%d cached) - downloading", chats, matched, cached)
	}

	return fmt.Sprintf("walked %d chats, matched %d files - downloading", chats, matched)
}

// printWalkTransition writes the dim handover line to stderr after the
// walk surface closed and before the download reporter opens; silent
// mode stays quiet.
func printWalkTransition(cmd *cobra.Command, app *App, chats, matched int, cached int64) error {
	if app.silentMode(cmd) {
		return nil
	}

	line := app.errStyle.Dim(walkTransition(chats, matched, cached))

	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", line); err != nil {
		return fmt.Errorf("print walk transition: %w", err)
	}

	return nil
}

// cachedTotal sums the prior-run manifest counts the incremental walk
// stashed per chat.
func cachedTotal(collector *walkCollector) int64 {
	var cached int64

	for _, value := range collector.cached {
		cached += value
	}

	return cached
}

// takeoutPoolsNotice explains the single-connection download mode under an
// active takeout session.
const takeoutPoolsNotice = "downloads ride the takeout session " +
	"(single connection; parallel pools resume on non-takeout runs)"

// printTakeoutPoolsNotice tells the user downloads skip parallel pools for
// the takeout rate-limit path; silent mode stays quiet.
func printTakeoutPoolsNotice(cmd *cobra.Command, app *App, silent bool) error {
	if silent {
		return nil
	}

	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Dim(takeoutPoolsNotice)); err != nil {
		return fmt.Errorf("print takeout download notice: %w", err)
	}

	return nil
}

// newDownloadReporter picks the progress surface: the bubbletea live view
// when stdout is a terminal, one line per settled transfer otherwise, and
// nothing at all in silent mode. --no-ascii forces the line-per-item
// surface even on terminals (the live view repaints with ANSI escapes).
// The returned closer finalizes the UI.
//
//nolint:ireturn // the Reporter interface is the Manager's progress contract
func newDownloadReporter(cmd *cobra.Command, silent, noASCII bool) (download.Reporter, func()) {
	if silent {
		return download.NoopReporter{}, func() {}
	}

	if !noASCII && term.IsTerminal(int(os.Stdout.Fd())) {
		live := download.NewLiveReporter(cmd.ErrOrStderr())

		return live, live.Close
	}

	return download.NewQuietReporter(cmd.ErrOrStderr()), func() {}
}

// advanceWatermarks moves each chat forward to its highest walked message
// id when that chat saw no failures; failed chats keep their watermark so
// ClaimPending retries their rows on the next sync. Silent mode suppresses
// the per-chat advancement lines.
func advanceWatermarks(ctx context.Context, cmd *cobra.Command, state *store.Store,
	res download.Result, collector *walkCollector, targets []scan.Target, silent bool,
) error {
	for _, target := range targets {
		highest, seen := collector.maxSeen[target.Chat.ID]
		if !seen || res.FailedByChat[target.Chat.ID] > 0 {
			continue
		}

		if err := state.AdvanceWatermark(ctx, target.Chat.ID, highest, time.Now()); err != nil {
			return fmt.Errorf("advance watermark chat %d: %w", target.Chat.ID, err)
		}

		if silent {
			continue
		}

		if err := printLine(cmd, "watermark %d -> %d\n", target.Chat.ID, highest); err != nil {
			return err
		}
	}

	return nil
}

// advanceWalkedWatermarks moves every chat that yielded a message this run
// to its highest walked id; preview walks have no failure signal, so every
// walked chat advances.
func advanceWalkedWatermarks(
	ctx context.Context, state *store.Store, collector *walkCollector, targets []scan.Target,
) error {
	for _, target := range targets {
		highest, seen := collector.maxSeen[target.Chat.ID]
		if !seen {
			continue
		}

		if err := state.AdvanceWatermark(ctx, target.Chat.ID, highest, time.Now()); err != nil {
			return fmt.Errorf("advance watermark chat %d: %w", target.Chat.ID, err)
		}
	}

	return nil
}

// withCachedPending appends to the freshly walked items the manifest rows
// of incrementally walked chats that still owe a download (discovered,
// queued, failed), skipping rows the current walk already produced.
func withCachedPending(
	ctx context.Context, state *store.Store, targets []scan.Target, collector *walkCollector,
) ([]store.MediaItem, error) {
	merged := make([]store.MediaItem, 0, len(collector.items))
	merged = append(merged, collector.items...)

	fresh := make(map[itemKey]bool, len(merged))
	for _, item := range merged {
		fresh[mediaItemKey(item)] = true
	}

	for _, target := range targets {
		if !collector.incremental[target.Chat.ID] {
			continue
		}

		pending, err := state.PendingMedia(ctx, target.Chat.ID)
		if err != nil {
			return nil, fmt.Errorf("load pending manifest rows chat %d: %w", target.Chat.ID, err)
		}

		for _, row := range pending {
			if fresh[mediaItemKey(*row)] {
				continue
			}

			fresh[mediaItemKey(*row)] = true

			merged = append(merged, *row)
		}
	}

	return merged, nil
}

// itemKey identifies one manifest row.
type itemKey struct {
	chatID    int64
	messageID int64
	mediaIdx  int
}

func mediaItemKey(item store.MediaItem) itemKey {
	return itemKey{chatID: item.ChatID, messageID: item.MessageID, mediaIdx: item.MediaIndex}
}

func finishStatus(ctx context.Context, state *store.Store, runID string, res download.Result) error {
	status := store.StatusDone
	if res.Failed > 0 {
		status = store.StatusFailed
	}

	if err := state.FinishRun(ctx, runID, status, ""); err != nil {
		return fmt.Errorf("finish run %s: %w", runID, err)
	}

	return nil
}

func finishRunE(ctx context.Context, state *store.Store, runID string, cause error) error {
	if err := state.FinishRun(ctx, runID, store.StatusFailed, cause.Error()); err != nil {
		return fmt.Errorf("finish run %s: %w; original failure: %w", runID, err, cause)
	}

	return cause
}

func chatFromTarget(target scan.Target) store.Chat {
	title := target.Chat.Title

	return store.Chat{
		ChatID:   target.Chat.ID,
		Type:     target.Chat.Type,
		Title:    &title,
		Username: stringOrNil(target.Chat.Username),
	}
}

func effectiveOptions(cfg *config.Config, cmd *cobra.Command, filterSet *filterFlags) (filters.Options, string, error) {
	profileName, _ := cmd.Flags().GetString("profile")

	opts := cfg.Filters

	if profileName != "" {
		profile, err := cfg.Profile(profileName)
		if err != nil {
			return filters.Options{}, "", fmt.Errorf("load profile: %w", err)
		}

		opts = *profile
	}

	overlayChangedFlags(cmd.Flags(), &opts, &filterSet.opts)

	return opts, profileName, nil
}

// overlayChangedFlags copies every explicitly-set filter flag from flagSrc
// onto dst; unchanged flags keep the config/profile value.
func overlayChangedFlags(flagSet *pflag.FlagSet, dst, flagSrc *filters.Options) {
	overlayChangedStruct(flagSet, reflect.ValueOf(dst).Elem(), reflect.ValueOf(flagSrc).Elem())
}

func overlayChangedStruct(flagSet *pflag.FlagSet, dst, src reflect.Value) {
	structType := dst.Type()

	for idx := range structType.NumField() {
		field := structType.Field(idx)
		tag := field.Tag.Get("flag")

		if tag == "" {
			if field.Type.Kind() == reflect.Struct {
				overlayChangedStruct(flagSet, dst.Field(idx), src.Field(idx))
			}

			continue
		}

		if flagSet.Lookup(tag) == nil || !flagSet.Changed(tag) {
			continue
		}

		dst.Field(idx).Set(src.Field(idx))
	}
}

func accountList(app *App, cmd *cobra.Command) ([]string, error) {
	value, _ := cmd.Flags().GetString("account")
	if value == "" {
		return []string{app.cfg.Auth.Account}, nil
	}

	if value == "all" {
		manager := tg.NewAccountManager(app.paths.AccountsDir)

		accounts, err := manager.List()
		if err != nil {
			return nil, fmt.Errorf("list accounts: %w", err)
		}

		return accounts, nil
	}

	return strings.Split(value, ","), nil
}

func encodeRunPayload(specs []string, opts filters.Options) (string, error) {
	encoded, err := toml.Marshal(runPayload{Chats: specs, teleparseOptionsEmbed: teleparseOptionsEmbed{Options: opts}})
	if err != nil {
		return "", fmt.Errorf("encode run payload: %w", err)
	}

	return string(encoded), nil
}

func newRunID() (string, error) {
	salt := make([]byte, runIDSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}

	return "run-" + time.Now().UTC().Format(runIDTimeFormat) + "-" + hex.EncodeToString(salt), nil
}

// Reporting helpers below render the plan, counts and summary tables.

func printExplain(cmd *cobra.Command, plan *filters.Plan) error {
	writer := tabWriter(cmd)

	if _, err := fmt.Fprintf(writer, "pushdown:\tmessages_filter=%s query=%q min_date=%d max_date=%d\n",
		plan.Pushdown.MessagesFilter, plan.Pushdown.SearchQuery, plan.Pushdown.MinDate, plan.Pushdown.MaxDate); err != nil {
		return fmt.Errorf("write explain: %w", err)
	}

	for _, note := range plan.Pushdown.Notes {
		if _, err := fmt.Fprintf(writer, "note:\t%s\n", note); err != nil {
			return fmt.Errorf("write explain note: %w", err)
		}
	}

	for _, line := range plan.ExplainLines {
		if _, err := fmt.Fprintf(writer, "%s\n", line); err != nil {
			return fmt.Errorf("write explain line: %w", err)
		}
	}

	return flushWriter(writer, cmd)
}

func printPlan(cmd *cobra.Command, collector *walkCollector) error {
	writer := tabWriter(cmd)

	if _, err := fmt.Fprintln(writer, "CHAT\tMSG\tKIND\tSIZE\tNAME"); err != nil {
		return fmt.Errorf("write plan header: %w", err)
	}

	for idx := range collector.items {
		item := collector.items[idx]

		if _, err := fmt.Fprintf(writer, "%d\t%d\t%s\t%s\t%s\n",
			item.ChatID, item.MessageID, item.MediaClass, humanSize(item.Size), textOrDefault(item.Filename)); err != nil {
			return fmt.Errorf("write plan row: %w", err)
		}
	}

	if err := flushWriter(writer, cmd); err != nil {
		return err
	}

	cached := cachedTotal(collector)

	if cached == 0 {
		return printLine(cmd, "total: %d file(s), %s\n", len(collector.items), humanTotalSize(collector))
	}

	return printLine(cmd, "total: %d file(s), %s (+%d cached from previous runs; rows above list new matches only)\n",
		len(collector.items), humanTotalSize(collector), cached)
}

// printSummary renders the final one-line dl/sync outcome; the linked
// counter appears only when hardlink dedupe served occurrences this run.
func printSummary(cmd *cobra.Command, res download.Result, took time.Duration) error {
	interrupted := ""
	if res.Interrupted > 0 {
		interrupted = fmt.Sprintf(", interrupted: %d (resumable)", res.Interrupted)
	}

	if res.Linked > 0 {
		return printLine(cmd,
			"downloaded: %d (%s), linked: %d, skipped: %d, failed: %d%s, retries: %d, took %s\n",
			res.Downloaded, humanBytes(res.Bytes), res.Linked, res.Skipped, res.Failed, interrupted,
			res.Retries, took.Round(time.Second))
	}

	return printLine(cmd, "downloaded: %d (%s), skipped: %d, failed: %d%s, retries: %d, took %s\n",
		res.Downloaded, humanBytes(res.Bytes), res.Skipped, res.Failed, interrupted, res.Retries,
		took.Round(time.Second))
}

func countFor(collector *walkCollector, chatID int64) int {
	count := 0

	for idx := range collector.items {
		if collector.items[idx].ChatID == chatID {
			count++
		}
	}

	return count
}

func tabWriter(cmd *cobra.Command) *tabwriter.Writer {
	return tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
}

func flushWriter(writer *tabwriter.Writer, cmd *cobra.Command) error {
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush table to %s output: %w", cmd.Name(), err)
	}

	return nil
}

func humanSize(value *int64) string {
	if value == nil {
		return "?"
	}

	return humanBytes(*value)
}

func humanTotalSize(collector *walkCollector) string {
	var total int64

	for idx := range collector.items {
		if collector.items[idx].Size != nil {
			total += *collector.items[idx].Size
		}
	}

	return humanBytes(total)
}

const kib = 1024.0

// sizeUnit pairs a divisor with its label.
type sizeUnit struct {
	divisor float64
	label   string
}

func sizeUnits() []sizeUnit {
	return []sizeUnit{{kib * kib * kib, "GiB"}, {kib * kib, "MiB"}, {kib, "KiB"}}
}

func humanBytes(value int64) string {
	scaled := float64(value)

	for _, unit := range sizeUnits() {
		if scaled >= unit.divisor {
			return fmt.Sprintf("%.1f%s", scaled/unit.divisor, unit.label)
		}
	}

	return fmt.Sprintf("%dB", value)
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func stringOrNil(text string) *string {
	if text == "" {
		return nil
	}

	return &text
}

// filterMinAgeWithProgress runs the chat-age probe with a live progress
// line on stderr (single \r-rewritten line on a terminal, one line per
// chunk otherwise) so large dialog sets never look frozen; stdout stays
// machine-readable for --format json.
func filterMinAgeWithProgress(
	ctx context.Context,
	cmd *cobra.Command,
	app *App,
	api scan.WalkAPI,
	targets []scan.Target,
	minAge time.Duration,
	label string,
) ([]scan.Target, error) {
	report := minAgeProgressReporter(cmd, app, label, len(targets))

	kept, err := scan.FilterByMinAge(ctx, api, targets, minAge, report)

	if app != nil && !app.silentMode(cmd) {
		line := app.errStyle.Dim("chat-min-age "+label+":") + fmt.Sprintf(" probing %d chats... ", len(targets)) +
			app.errStyle.Success("done") + ", kept " +
			app.errStyle.Success(fmt.Sprintf("%d/%d", len(kept), len(targets))) + "\n"

		if _, err := fmt.Fprint(cmd.ErrOrStderr(), "\r"+line); err != nil {
			return nil, fmt.Errorf("print min-age summary: %w", err)
		}
	}

	return kept, err //nolint:wrapcheck // probe errors already carry chat context
}

// minAgeProgressReporter renders probe progress to stderr: a single
// rewritten line on terminals, a chunked line otherwise, silent under -s.
func minAgeProgressReporter(cmd *cobra.Command, app *App, label string, total int) func(done, total int) {
	if app == nil || app.silentMode(cmd) || total == 0 {
		return nil
	}

	tty := term.IsTerminal(int(os.Stderr.Fd()))

	return func(done, all int) {
		if !tty && done%minAgeProgressChunk != 0 {
			return
		}

		line := app.errStyle.Dim("chat-min-age "+label+":") +
			fmt.Sprintf(" probing %d chats... %s", all, app.errStyle.Success(fmt.Sprintf("%d/%d", done, all)))

		if _, err := fmt.Fprint(cmd.ErrOrStderr(), "\r"+line); err != nil {
			return
		}

		if !tty {
			_, _ = fmt.Fprint(cmd.ErrOrStderr(), "\n")
		}
	}
}

// minAgeProgressChunk is the non-terminal progress reporting interval.
const minAgeProgressChunk = 25

// errTakeoutExclusive rejects combining --takeout with --no-takeout.
var errTakeoutExclusive = errors.New("--takeout and --no-takeout are mutually exclusive")

// Takeout export sessions carry a per-account file-size cap:
// account.initTakeoutSession takes it as file_max_size, and omitting it
// yields a session that allows no file bytes at all, so every download
// inside the session fails as TAKEOUT_FILE_TOO_BIG. Plain accounts cap at
// 2GiB, Telegram Premium at 4GiB.
const (
	takeoutFileCapBase    int64 = 2 << 30 // 2GiB
	takeoutFileCapPremium int64 = 4 << 30 // 4GiB
)

// takeoutCapFor maps the account's premium state onto the session cap.
func takeoutCapFor(premium bool) int64 {
	if premium {
		return takeoutFileCapPremium
	}

	return takeoutFileCapBase
}

// takeoutFileCap resolves the cap active for downloads: zero off takeout
// (no session cap applies), otherwise the account's premium cap.
func takeoutFileCap(takeoutActive, premium bool) int64 {
	if !takeoutActive {
		return 0
	}

	return takeoutCapFor(premium)
}

// takeoutConfigFor builds the export session config; FileMaxSize must
// carry the account cap or the server session allows no file bytes.
func takeoutConfigFor(premium bool) takeout.Config {
	return takeout.Config{
		Files:             true,
		MessageUsers:      true,
		MessageChats:      true,
		MessageMegagroups: true,
		MessageChannels:   true,
		FileMaxSize:       takeoutCapFor(premium),
	}
}

// takeoutCapNotice renders the cap line shown when a takeout session
// engages; an unknown premium answer notes the conservative default.
func takeoutCapNotice(status tg.PremiumStatus) string {
	if status.Premium {
		return "takeout file cap: " + humanBytes(takeoutFileCapPremium) + " (premium)"
	}

	if status.Source == tg.PremiumSourceNone {
		return "takeout file cap: " + humanBytes(takeoutFileCapBase) +
			" (premium unknown, base cap; premium raises it to " + humanBytes(takeoutFileCapPremium) + ")"
	}

	return "takeout file cap: " + humanBytes(takeoutFileCapBase) +
		" (premium raises it to " + humanBytes(takeoutFileCapPremium) + ")"
}

// resolveTakeoutModeFor decides whether this run rides a takeout session:
// explicit flags win, config takeout forces always, otherwise the auto
// heuristic engages when the estimated scope looks large. The probe is
// fail-soft: on error auto is skipped, never blocking the run.
func resolveTakeoutModeFor(
	flags dlRunFlags,
	cfg *config.Config,
	specs []string,
	probe func() (int, error),
) (bool, string) {
	switch {
	case flags.takeout:
		return true, "forced by --takeout"
	case flags.noTakeout:
		return false, "disabled by --no-takeout"
	case cfg.Net.Takeout:
		return true, "net.takeout = true"
	case !cfg.Net.TakeoutAuto:
		return false, "net.takeout_auto = false"
	}

	dialogs, err := probe()
	if err != nil {
		return false, "dialog count probe failed, auto skipped"
	}

	estimate := estimateScopeSize(specs, dialogs)

	if estimate >= cfg.Net.TakeoutAutoMinChats {
		return true, fmt.Sprintf("auto: ~%d chats >= %d", estimate, cfg.Net.TakeoutAutoMinChats)
	}

	return false, fmt.Sprintf("auto: ~%d chats < %d", estimate, cfg.Net.TakeoutAutoMinChats)
}

// estimateScopeSize approximates how many chats a scan will walk: "all"
// and title globs imply the whole dialog set, explicit specs count one
// chat each.
func estimateScopeSize(specs []string, dialogs int) int {
	estimate := 0

	for _, spec := range specs {
		switch {
		case spec == "all":
			return dialogs
		case strings.ContainsAny(spec, "*?["):
			return dialogs
		default:
			estimate++
		}
	}

	return estimate
}
