package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
)

// accountSessionRunner opens one connected client session per account; a
// package var so internal tests can fake the transport without a network.
var accountSessionRunner = tg.Run //nolint:gochecknoglobals // test seam

// routingRoute is one parsed routing-table entry: the original chat spec
// (reused verbatim as the routed session's scope spec), its matching form
// and the owning account.
type routingRoute struct {
	spec    string
	ref     scan.PeerRef
	account string
}

// routingPlan is a validated [accounts] routing table.
type routingPlan struct {
	routes []routingRoute
}

// parseRouting parses the routing table's chat specs through the scope
// spec parsers so routing keys resolve exactly like dl scope specs; keys
// are visited sorted so error reporting and route order stay deterministic.
func parseRouting(table map[string]string) (*routingPlan, error) {
	plan := &routingPlan{}

	for _, spec := range slices.Sorted(maps.Keys(table)) {
		ref, err := scan.ParsePeerSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("accounts.routing[%q]: %w", spec, err)
		}

		plan.routes = append(plan.routes, routingRoute{spec: spec, ref: ref, account: table[spec]})
	}

	return plan, nil
}

// errRoutingUnknownAccount marks a routing entry whose target account has
// no local session; validateAccounts wraps it with the spec, the known
// accounts and the login hint.
var errRoutingUnknownAccount = errors.New("no local session for the routed account")

// validateAccounts canonicalizes every route's account name and rejects
// accounts without a local session up front — before any chat resolves —
// so a typo fails the invocation at start, never mid-run.
func (p *routingPlan) validateAccounts(manager *tg.AccountManager) error {
	known, err := manager.List()
	if err != nil {
		return fmt.Errorf("list local accounts: %w", err)
	}

	sessions := map[string]bool{}
	for _, name := range known {
		sessions[name] = true
	}

	for idx := range p.routes {
		canonical, err := manager.NormalizeName(p.routes[idx].account)
		if err != nil {
			return fmt.Errorf("accounts.routing[%q]: %w", p.routes[idx].spec, err)
		}

		p.routes[idx].account = canonical

		if !sessions[canonical] {
			return fmt.Errorf("accounts.routing[%q]: account %q: %w (known: %s); "+
				"create it with `teleparse auth login --account %s`",
				p.routes[idx].spec, canonical, errRoutingUnknownAccount,
				strings.Join(known, ", "), canonical)
		}
	}

	return nil
}

// split partitions resolved targets: chats matched by a routing key leave
// the default account's pass and return grouped as account -> routing-key
// specs (in table order) for the routed session to process instead.
func (p *routingPlan) split(targets []scan.Target) ([]scan.Target, map[string][]string) {
	kept := make([]scan.Target, 0, len(targets))

	routed := map[string][]string{}

	for _, target := range targets {
		matched := false

		for _, route := range p.routes {
			if !route.ref.Matches(target.Chat.Username, target.Chat.ID) {
				continue
			}

			routed[route.account] = append(routed[route.account], route.spec)

			matched = true

			break
		}

		if !matched {
			kept = append(kept, target)
		}
	}

	return kept, routed
}

// splitRoutedTargets applies per-chat account routing to a resolved walk
// target list: chats matched by a routing key leave the default account's
// pass (recorded on extras for the routed session) with the exclusion
// notice printed; get jobs and absent plans return targets untouched.
func splitRoutedTargets(
	cmd *cobra.Command, app *App, extras *invocationExtras, job *getJob, targets []scan.Target,
) ([]scan.Target, error) {
	if extras == nil || extras.routing == nil || job != nil {
		return targets, nil
	}

	var routed map[string][]string

	targets, routed = extras.routing.split(targets)
	if len(routed) == 0 {
		return targets, nil
	}

	extras.recordRouted(routed)

	if err := printRoutingExclusionNotice(cmd, app, routed); err != nil {
		return nil, err
	}

	return targets, nil
}

// invocationExtras carries cross-session state for one dl-family
// invocation: the default pass excludes routed chats and records them,
// downloadRun defers oversized items, and afterwards runDeferredSessions
// opens one sequential session per recorded account. A nil extras (get
// jobs, resume, and the deferred passes themselves) disables both.
type invocationExtras struct {
	routing     *routingPlan
	routed      map[string][]string
	premiumAcct string
	deferrals   []int64
}

// recordRouted merges the specs a pass excluded into the per-account map.
func (e *invocationExtras) recordRouted(routed map[string][]string) {
	for account, specs := range routed {
		e.routed[account] = append(e.routed[account], specs...)
	}
}

// recordDeferrals collects the chat ids of items deferred to the premium
// pass, one entry per chat.
func (e *invocationExtras) recordDeferrals(items []store.MediaItem) {
	for _, item := range items {
		if !slices.Contains(e.deferrals, item.ChatID) {
			e.deferrals = append(e.deferrals, item.ChatID)
		}
	}
}

// planInvocation resolves what this invocation defers to later sessions:
// nothing for get jobs (explicit link contexts do not route), nothing
// when --account pins accounts (precedence: --account > routing), no
// routing table under --no-routing, and premium preference only when the
// config enables it. Routing tables are fully validated here so an
// unknown account fails before the first session opens.
func planInvocation(
	ctx context.Context, app *App, cmd *cobra.Command, flags dlRunFlags, job *getJob,
) (*invocationExtras, error) {
	if job != nil {
		return nil, nil //nolint:nilnil // a nil plan legitimately means "nothing deferred"
	}

	if pinned, _ := cmd.Flags().GetString("account"); pinned != "" {
		return nil, nil //nolint:nilnil // a nil plan legitimately means "nothing deferred"
	}

	var routing *routingPlan

	if len(app.cfg.Accounts.Routing) > 0 && !flags.noRouting {
		parsed, err := parseRouting(app.cfg.Accounts.Routing)
		if err != nil {
			return nil, err
		}

		if err := parsed.validateAccounts(tg.NewAccountManager(app.paths.AccountsDir)); err != nil {
			return nil, err
		}

		routing = parsed
	}

	var premiumAcct string

	if app.cfg.Accounts.PremiumPreferred {
		premiumAcct = findLocalPremium(ctx, app.paths.AccountsDir)
	}

	if routing == nil && !app.cfg.Accounts.PremiumPreferred {
		return nil, nil //nolint:nilnil // a nil plan legitimately means "nothing deferred"
	}

	return &invocationExtras{routing: routing, routed: map[string][]string{}, premiumAcct: premiumAcct}, nil
}

// findLocalPremium returns the first local account whose cached premium
// state says Telegram Premium, or "" when none does; detection is
// cache-only (no RPC) and fail-soft, mirroring the session cap resolution.
func findLocalPremium(ctx context.Context, accountsDir string) string {
	manager := tg.NewAccountManager(accountsDir)

	accounts, err := manager.List()
	if err != nil {
		return ""
	}

	for _, name := range accounts {
		if manager.AccountPremium(ctx, name, nil).Premium {
			return name
		}
	}

	return ""
}

// premiumDeferralCap resolves the per-file byte cap the current session
// can actually transfer: the active takeout cap when one runs, else the
// account's server-side limit (4GiB premium, 2GiB base).
func premiumDeferralCap(takeoutCap int64, premium bool) int64 {
	if takeoutCap > 0 {
		return takeoutCap
	}

	return takeoutCapFor(premium)
}

// partitionPremiumDeferred splits items into (main pass, deferred): an
// item whose known size exceeds the current session's cap but fits the
// premium 4GiB cap moves to the deferred slice for the premium pass;
// larger items, unknown sizes and in-cap items always stay.
func partitionPremiumDeferred(items []store.MediaItem, currentCap int64) ([]store.MediaItem, []store.MediaItem) {
	main := make([]store.MediaItem, 0, len(items))

	var deferred []store.MediaItem

	for _, item := range items {
		size := item.Size
		if size != nil && *size > currentCap && *size <= takeoutFileCapPremium {
			deferred = append(deferred, item)

			continue
		}

		main = append(main, item)
	}

	return main, deferred
}

// deferOversizedToPremium moves items the current session cannot transfer
// but a premium 4GiB session can out of the main queue, recording them on
// extras for the premium pass; a missing plan or premium account is a
// no-op (the items then fail with the cap reason as before).
func deferOversizedToPremium(
	cmd *cobra.Command,
	app *App,
	extras *invocationExtras,
	premium bool,
	takeoutCap int64,
	items []store.MediaItem,
) ([]store.MediaItem, error) {
	if extras == nil || extras.premiumAcct == "" {
		return items, nil
	}

	var deferred []store.MediaItem

	items, deferred = partitionPremiumDeferred(items, premiumDeferralCap(takeoutCap, premium))
	if len(deferred) == 0 {
		return items, nil
	}

	extras.recordDeferrals(deferred)

	if err := printPremiumDeferralNotice(cmd, app, extras.premiumAcct, deferred); err != nil {
		return nil, err
	}

	return items, nil
}

// deferredPremiumSpecs renders deferred chat ids as numeric scope specs,
// deduplicated and sorted, for the premium pass's session.
func deferredPremiumSpecs(chatIDs []int64) []string {
	unique := slices.Compact(slices.Sorted(slices.Values(chatIDs)))

	specs := make([]string, 0, len(unique))
	for _, chatID := range unique {
		specs = append(specs, strconv.FormatInt(chatID, 10))
	}

	return specs
}

// passKind names why a deferred session opens.
type passKind string

const (
	passRouting passKind = "routing"
	passPremium passKind = "premium"
)

// deferredPass is one sequential session owed after the default pass.
type deferredPass struct {
	kind    passKind
	account string
	specs   []string
}

// routingPasses lists the sequential routed-account sessions owed after
// the default pass, accounts sorted by name.
func routingPasses(extras *invocationExtras) []deferredPass {
	if extras == nil {
		return nil
	}

	var passes []deferredPass

	for _, account := range slices.Sorted(maps.Keys(extras.routed)) {
		passes = append(passes, deferredPass{kind: passRouting, account: account, specs: extras.routed[account]})
	}

	return passes
}

// premiumPasses lists the premium oversized pass owed, if any.
func premiumPasses(extras *invocationExtras) []deferredPass {
	if extras == nil || len(extras.deferrals) == 0 || extras.premiumAcct == "" {
		return nil
	}

	return []deferredPass{{
		kind: passPremium, account: extras.premiumAcct, specs: deferredPremiumSpecs(extras.deferrals),
	}}
}

// deferredPasses orders the owed sessions: routed accounts sorted by
// name, then one premium pass for the deferred oversized items. Nil or
// empty extras yield no passes.
func deferredPasses(extras *invocationExtras) []deferredPass {
	return append(routingPasses(extras), premiumPasses(extras)...)
}

// runDeferredSessions opens one sequential client session per owed pass —
// routed accounts first, then the premium oversized pass — reusing the
// standard account-session machinery. Routing is stripped for these
// passes (their chats are already routed) but premium preference stays
// on, so oversized items discovered by a routed pass defer to the premium
// pass too; sessions never run in parallel, and the per-account flock
// would forbid that anyway.
func runDeferredSessions(
	cmd *cobra.Command,
	app *App,
	creds tg.Creds,
	extras *invocationExtras,
	profileName string,
	opts filters.Options,
	plan *filters.Plan,
	mode runMode,
	flags dlRunFlags,
) error {
	if extras == nil {
		return nil
	}

	run := func(pass deferredPass, sessionExtras *invocationExtras) error {
		if err := printDeferredPassNotice(cmd, app, pass); err != nil {
			return err
		}

		cfg := *app.cfg
		cfg.Auth.Account = pass.account

		runErr := accountSessionRunner(cmd.Context(), pass.account, creds, &cfg, app.paths, func(
			ctx context.Context,
			client *telegram.Client,
		) error {
			return runAccountSession(ctx, cmd, app, pass.account, profileName, opts, plan, pass.specs,
				mode, client, &cfg, flags, sessionExtras, nil)
		})
		if runErr != nil {
			return fmt.Errorf("%s pass (account %s): %w", pass.kind, pass.account, runErr)
		}

		return nil
	}

	routedExtras := &invocationExtras{premiumAcct: extras.premiumAcct}

	for _, pass := range routingPasses(extras) {
		if err := run(pass, routedExtras); err != nil {
			return err
		}
	}

	extras.deferrals = append(extras.deferrals, routedExtras.deferrals...)

	for _, pass := range premiumPasses(extras) {
		if err := run(pass, nil); err != nil {
			return err
		}
	}

	return nil
}

// deferredPassNotice renders the one-line dim notice for an owed pass;
// empty for unknown kinds (never reached in practice).
func deferredPassNotice(pass deferredPass) string {
	switch pass.kind {
	case passRouting:
		return fmt.Sprintf("routing pass: account %s - %s", pass.account, strings.Join(pass.specs, ", "))
	case passPremium:
		return fmt.Sprintf("premium pass: account %s - %d deferred oversized chat(s)",
			pass.account, len(pass.specs))
	default:
		return ""
	}
}

// printDeferredPassNotice writes the dim pass handover line to stderr;
// silent mode stays quiet.
func printDeferredPassNotice(cmd *cobra.Command, app *App, pass deferredPass) error {
	if app.silentMode(cmd) {
		return nil
	}

	line := app.errStyle.Dim(deferredPassNotice(pass))

	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", line); err != nil {
		return fmt.Errorf("print %s pass notice: %w", pass.kind, err)
	}

	return nil
}

// printRoutingExclusionNotice tells the user which chats left the default
// pass for routed accounts; silent mode stays quiet.
func printRoutingExclusionNotice(cmd *cobra.Command, app *App, routed map[string][]string) error {
	if app.silentMode(cmd) {
		return nil
	}

	accounts := slices.Sorted(maps.Keys(routed))

	parts := make([]string, 0, len(accounts))
	for _, account := range accounts {
		parts = append(parts, strings.Join(routed[account], ", ")+" -> account "+account)
	}

	line := app.errStyle.Dim("routing: excluding " + strings.Join(parts, "; ") +
		" from this pass (processed after it)")

	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", line); err != nil {
		return fmt.Errorf("print routing notice: %w", err)
	}

	return nil
}

// printPremiumDeferralNotice tells the user oversized items moved out of
// this pass for the premium session; silent mode stays quiet.
func printPremiumDeferralNotice(
	cmd *cobra.Command, app *App, account string, items []store.MediaItem,
) error {
	if app.silentMode(cmd) {
		return nil
	}

	chats := make([]string, 0, len(items))
	for _, item := range items {
		chats = append(chats, strconv.FormatInt(item.ChatID, 10))
	}

	line := app.errStyle.Dim(fmt.Sprintf(
		"premium-preferred: %d oversized item(s) deferred to account %s (chat(s): %s)",
		len(items), account, strings.Join(chats, ", ")))

	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", line); err != nil {
		return fmt.Errorf("print premium deferral notice: %w", err)
	}

	return nil
}
