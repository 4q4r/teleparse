package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/filters"
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
}

// runMode tunes the shared pipeline: dry-run previews, count-only output
// and sync-mode watermarks.
type runMode struct {
	dryRun    bool
	countOnly bool
	syncMode  bool
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
				dryRun:    flags.dryRun,
				countOnly: flags.countOnly,
			}, "")
		},
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "list matches, download nothing (same as scan)")
	cmd.Flags().BoolVar(&flags.explain, "explain", false,
		"print which filters push down to the server vs run client-side, then proceed")
	cmd.Flags().BoolVar(&flags.countOnly, "count-only", false, "only print per-chat match counts")
	cmd.Flags().BoolVar(&flags.takeout, "takeout", false, "wrap session in takeout mode (lower flood limits)")
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
			return runDownloadCommand(app, c, args, &filterSet, flags, runMode{dryRun: true}, "")
		},
	}
	cmd.Flags().BoolVar(&flags.explain, "explain", false,
		"print which filters push down to the server vs run client-side, then proceed")
	cmd.Flags().BoolVar(&flags.countOnly, "count-only", false, "only print per-chat match counts")
	addSilentOutputMirror(cmd)
	addFilterFlags(cmd, &filterSet)

	return cmd
}

// runDownloadCommand resolves effective options and accounts, then executes
// the pipeline once per account; accountOverride pins the account (resume).
func runDownloadCommand(app *App, cmd *cobra.Command, specs []string, filterSet *filterFlags,
	flags dlRunFlags, mode runMode, accountOverride string,
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

	for _, account := range accounts {
		cfg := *app.cfg
		cfg.Auth.Account = account

		runErr := tg.Run(cmd.Context(), account, creds, &cfg, app.paths, func(
			ctx context.Context,
			client *telegram.Client,
		) error {
			return withAPI(ctx, client, flags.takeout || cfg.Net.Takeout, func(
				ctx context.Context,
				api *tgapi.Client,
			) error {
				return executeRun(ctx, cmd, app, account, profileName, opts, plan, specs, mode, api, client)
			})
		})
		if runErr != nil {
			return fail(cmd, runErr)
		}
	}

	return nil
}

// withAPI runs fn with the raw API client, wrapped in a takeout session
// when requested so downloads ride the export rate-limit path.
func withAPI(ctx context.Context, client *telegram.Client, takeoutMode bool,
	runAPI func(context.Context, *tgapi.Client) error,
) error {
	if !takeoutMode {
		return runAPI(ctx, client.API())
	}

	if err := takeout.Run(ctx, client, takeout.Config{
		Files:             true,
		MessageUsers:      true,
		MessageChats:      true,
		MessageMegagroups: true,
		MessageChannels:   true,
	}, func(ctx context.Context, session *takeout.Client) error {
		return runAPI(ctx, tgapi.NewClient(session))
	}); err != nil {
		return fmt.Errorf("run takeout session: %w", err)
	}

	return nil
}

// executeRun walks the resolved scope, records the run, then previews or
// downloads everything the filters matched; decomposed into helpers below.
func executeRun(ctx context.Context, cmd *cobra.Command, app *App, account, profileName string,
	opts filters.Options, plan *filters.Plan, specs []string, mode runMode,
	api *tgapi.Client, client *telegram.Client,
) error {
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

	minAge, err := scan.MinAgeFromOptions(opts)
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	if minAge > 0 {
		if err := printLine(cmd, "chat-min-age %s: probing %d chats...\n", opts.ChatMinAge, len(targets)); err != nil {
			return finishRunE(ctx, state, runID, fmt.Errorf("print min-age status: %w", err))
		}

		targets, err = scan.FilterByMinAge(ctx, api, targets, minAge)
		if err != nil {
			return finishRunE(ctx, state, runID, err)
		}
	}

	resolver, collector := newRunResolver(app, api)

	for _, target := range targets {
		if err := state.UpsertChat(ctx, chatFromTarget(target)); err != nil {
			return finishRunE(ctx, state, runID, err)
		}

		resolver.peers[target.Chat.ID] = target.InputPeer

		if err := walkTarget(ctx, state, api, target, plan, opts, mode, collector); err != nil {
			return finishRunE(ctx, state, runID, err)
		}
	}

	if mode.dryRun || mode.countOnly {
		return previewRun(ctx, cmd, state, runID, collector, targets, mode)
	}

	return downloadRun(ctx, cmd, state, app, runID, account, resolver, collector, targets, mode, opts, api, client)
}

// walkCollector accumulates manifest items, walk context and per-chat
// message-id highs while the walk runs.
type walkCollector struct {
	cache   *messageCache
	items   []store.MediaItem
	maxSeen map[int64]int64
}

func walkTarget(ctx context.Context, state *store.Store, api *tgapi.Client, target scan.Target,
	plan *filters.Plan, opts filters.Options, mode runMode, collector *walkCollector,
) error {
	walkOpts := opts

	if mode.syncMode {
		watermark, err := state.Watermark(ctx, target.Chat.ID)
		if err != nil {
			return fmt.Errorf("read watermark chat %d: %w", target.Chat.ID, err)
		}

		walkOpts.MinID = watermark
		walkOpts.Reverse = true
	}

	emit := func(fctx filters.Context, msg *tgapi.Message) error {
		collector.observe(fctx, msg)

		return nil
	}

	walker := scan.NewHistoryWalker(api, scan.HistoryFeeds(api), scan.NewSenderCache(api))

	if err := walker.Walk(ctx, target, plan, walkOpts, emit); err != nil {
		return fmt.Errorf("walk %q: %w", target.Chat.Title, err)
	}

	return nil
}

func (c *walkCollector) observe(fctx filters.Context, msg *tgapi.Message) {
	if fctx.Message.ID > c.maxSeen[fctx.Chat.ID] {
		c.maxSeen[fctx.Chat.ID] = fctx.Message.ID
	}

	if msg == nil {
		return
	}

	c.cache.put(msgCacheKey{chatID: fctx.Chat.ID, msgID: fctx.Message.ID}, walkedMessage{fctx: fctx, msg: msg})

	item, ok := mediaItemFromMessage(fctx, msg)
	if !ok {
		return
	}

	c.items = append(c.items, item)
}

func newRunResolver(app *App, api refetchAPI) (*runResolver, *walkCollector) {
	cache := newMessageCache(msgCacheLimit)

	return &runResolver{
		root:     app.paths.Downloads,
		template: app.cfg.Output.Template,
		cache:    cache,
		peers:    map[int64]tgapi.InputPeerClass{},
		api:      api,
	}, &walkCollector{cache: cache, maxSeen: map[int64]int64{}}
}

func previewRun(ctx context.Context, cmd *cobra.Command, state *store.Store, runID string,
	collector *walkCollector, targets []scan.Target, mode runMode,
) error {
	for idx := range collector.items {
		item := collector.items[idx]
		item.Status = store.StatusDiscovered

		if err := state.UpsertMedia(ctx, &item); err != nil {
			return finishRunE(ctx, state, runID, err)
		}
	}

	if mode.countOnly {
		if err := printCounts(cmd, collector, targets); err != nil {
			return err
		}
	} else if err := printPlan(cmd, collector); err != nil {
		return err
	}

	if err := state.FinishRun(ctx, runID, store.StatusDone, ""); err != nil {
		return fmt.Errorf("finish run %s: %w", runID, err)
	}

	return nil
}

func downloadRun(ctx context.Context, cmd *cobra.Command, state *store.Store, app *App, runID string,
	account string, resolver *runResolver, collector *walkCollector, targets []scan.Target,
	mode runMode, opts filters.Options, api *tgapi.Client, client *telegram.Client,
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

	threads, connections := app.cfg.Download.Effective(premium.Premium)

	if !silent && (threads != app.cfg.Download.Threads || connections != app.cfg.Download.Connections) {
		if err := printLine(cmd, "premium boost: threads %d, connections %d (source: %s)\n",
			threads, connections, premium.Source); err != nil {
			return err
		}
	}

	// Parallel-connection engine: per-DC media pools with a home-DC
	// fallback; pool failures degrade to the single primary connection.
	pools := tg.NewDownloadPools(client, int64(connections))

	defer func() { _ = pools.Close() }()

	mgr := download.NewManager(state, pacer, download.Config{
		Output:       app.cfg.Output,
		Hooks:        app.cfg.Hooks,
		Concurrency:  app.cfg.Pacing.Concurrency,
		RetryMax:     app.cfg.Pacing.RetryMax,
		Dedupe:       opts.Dedupe,
		SkipExisting: opts.SkipExisting,
	}, resolver, reporter)
	mgr.Fetch = download.ParallelFetch(pools, api, download.ParallelOptions{
		Threads:  threads,
		Reporter: reporter,
	})

	if err := mgr.Enqueue(ctx, collector.items); err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	reporter.SetPhase("downloading")

	started := time.Now()

	res, err := mgr.Run(ctx, runID)
	if err != nil {
		return finishRunE(ctx, state, runID, err)
	}

	if res.Parked {
		return printLine(cmd, "parked: flood wait until %s — resume with `teleparse resume %s`\n",
			res.ResumeAt.Format(time.RFC3339), runID)
	}

	if err := finishStatus(ctx, state, runID, res); err != nil {
		return err
	}

	// The live UI prints its own final summary on close; quiet mode keeps
	// the stdout one-liner; silent prints neither.
	_, live := reporter.(*download.LiveReporter)

	if !silent && !live {
		if err := printSummary(cmd, res, time.Since(started)); err != nil {
			return err
		}
	}

	if mode.syncMode {
		return advanceWatermarks(ctx, cmd, state, res, collector, targets, silent)
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

	return printLine(cmd, "total: %d file(s), %s\n", len(collector.items), humanTotalSize(collector))
}

func printCounts(cmd *cobra.Command, collector *walkCollector, targets []scan.Target) error {
	writer := tabWriter(cmd)

	if _, err := fmt.Fprintln(writer, "CHAT\tTITLE\tMATCHES"); err != nil {
		return fmt.Errorf("write counts header: %w", err)
	}

	for _, target := range targets {
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%d\n",
			target.Chat.ID, target.Chat.Title, countFor(collector, target.Chat.ID)); err != nil {
			return fmt.Errorf("write counts row: %w", err)
		}
	}

	return flushWriter(writer, cmd)
}

// printSummary renders the final one-line dl/sync outcome.
func printSummary(cmd *cobra.Command, res download.Result, took time.Duration) error {
	return printLine(cmd, "downloaded: %d (%s), skipped: %d, failed: %d, retries: %d, took %s\n",
		res.Downloaded, humanBytes(res.Bytes), res.Skipped, res.Failed, res.Retries, took.Round(time.Second))
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
