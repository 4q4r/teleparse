package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeSession materializes a local account (session.json) under dir.
func writeFakeSession(t *testing.T, dir, name string) {
	t.Helper()

	accountDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(accountDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(accountDir, "session.json"), []byte("{}"), 0o600))
}

// writeFakePremium seeds a fresh premium cache record for a local account.
func writeFakePremium(t *testing.T, dir, name string, premium bool) {
	t.Helper()

	accountDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(accountDir, 0o700))

	record := struct {
		Premium   bool      `json:"premium"`
		CheckedAt time.Time `json:"checked_at"`
	}{Premium: premium, CheckedAt: time.Now()}

	blob, err := json.Marshal(record)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(accountDir, "premium.json"), blob, 0o600))
}

func TestParseRoutingPlan(t *testing.T) {
	t.Parallel()

	plan, err := parseRouting(map[string]string{"@chat": "account2", "123456": "spare"})
	require.NoError(t, err)
	require.Len(t, plan.routes, 2)

	refs := map[string]scan.PeerRef{}
	for _, route := range plan.routes {
		refs[route.account] = route.ref
	}

	assert.Equal(t, scan.PeerRef{Username: "chat"}, refs["account2"])
	assert.Equal(t, scan.PeerRef{ID: 123456}, refs["spare"])
}

func TestParseRoutingPlanRejectsBadSpec(t *testing.T) {
	t.Parallel()

	_, err := parseRouting(map[string]string{"https://t.me/c/1/2": "spare"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accounts.routing")
	assert.Contains(t, err.Error(), "https://t.me/c/1/2")
}

func TestRoutingValidateAccountsUnknown(t *testing.T) {
	t.Parallel()

	accountsDir := t.TempDir()
	writeFakeSession(t, accountsDir, "spare")

	plan, err := parseRouting(map[string]string{"@chat": "ghost", "123": "spare"})
	require.NoError(t, err)

	err = plan.validateAccounts(tg.NewAccountManager(accountsDir))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"ghost"`)
	assert.Contains(t, err.Error(), "teleparse auth login --account ghost")
	assert.Contains(t, err.Error(), "spare", "the error must list known accounts")

	err = parseRoutingMust(t, map[string]string{"@chat": "spare"}).validateAccounts(tg.NewAccountManager(accountsDir))
	require.NoError(t, err)
}

// parseRoutingMust is a test helper that cannot fail.
func parseRoutingMust(t *testing.T, table map[string]string) *routingPlan {
	t.Helper()

	plan, err := parseRouting(table)
	require.NoError(t, err)

	return plan
}

func TestRoutingSplitExcludesRoutedTargets(t *testing.T) {
	t.Parallel()

	plan := parseRoutingMust(t, map[string]string{"@news": "acct2", "222": "spare"})

	targets := []scan.Target{
		{Chat: filters.Chat{ID: 111, Username: "news", Title: "News"}},
		{Chat: filters.Chat{ID: 222, Title: "No username chat"}},
		{Chat: filters.Chat{ID: 333, Username: "other", Title: "Other"}},
	}

	kept, routed := plan.split(targets)

	require.Len(t, kept, 1)
	assert.Equal(t, int64(333), kept[0].Chat.ID, "only unrouted chats stay in the default pass")

	assert.Equal(t, map[string][]string{
		"acct2": {"@news"},
		"spare": {"222"},
	}, routed, "routed specs keep their original routing-key form")
}

func TestPlanInvocationPrecedence(t *testing.T) {
	t.Parallel()

	newTestApp := func(accountsDir string, cfg *config.Config) *App {
		return &App{
			cfg:      cfg,
			paths:    &config.Paths{AccountsDir: accountsDir},
			errStyle: NewStyler(false),
		}
	}

	newFlaggedCmd := func(account string) *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().String("account", account, "")

		return cmd
	}

	accountsDir := t.TempDir()
	writeFakeSession(t, accountsDir, "default")
	writeFakeSession(t, accountsDir, "spare")
	writeFakeSession(t, accountsDir, "prem")
	writeFakePremium(t, accountsDir, "prem", true)

	routedCfg := config.Default()
	routedCfg.Accounts.Routing = map[string]string{"@chat": "spare"}

	premiumCfg := config.Default()
	premiumCfg.Accounts.PremiumPreferred = true

	job := &getJob{}

	t.Run("get job never routes", func(t *testing.T) {
		t.Parallel()

		extras, err := planInvocation(t.Context(), newTestApp(accountsDir, routedCfg), newFlaggedCmd(""), dlRunFlags{}, job)
		require.NoError(t, err)
		assert.Nil(t, extras)
	})

	t.Run("pinned account wins over routing", func(t *testing.T) {
		t.Parallel()

		extras, err := planInvocation(t.Context(), newTestApp(accountsDir, routedCfg), newFlaggedCmd("spare"), dlRunFlags{}, nil)
		require.NoError(t, err)
		assert.Nil(t, extras)
	})

	t.Run("no-routing flag bypasses routing only", func(t *testing.T) {
		t.Parallel()

		extras, err := planInvocation(t.Context(), newTestApp(accountsDir, routedCfg), newFlaggedCmd(""), dlRunFlags{noRouting: true}, nil)
		require.NoError(t, err)
		assert.Nil(t, extras, "routing bypassed with premium off leaves nothing to do")

		extras, err = planInvocation(t.Context(), newTestApp(accountsDir, premiumCfg), newFlaggedCmd(""), dlRunFlags{noRouting: true}, nil)
		require.NoError(t, err)
		require.NotNil(t, extras, "premium preference is independent of --no-routing")
		assert.Nil(t, extras.routing, "--no-routing must drop the routing table")
	})

	t.Run("default off yields no extras", func(t *testing.T) {
		t.Parallel()

		extras, err := planInvocation(t.Context(), newTestApp(accountsDir, config.Default()), newFlaggedCmd(""), dlRunFlags{}, nil)
		require.NoError(t, err)
		assert.Nil(t, extras)
	})

	t.Run("unknown routed account fails at plan time", func(t *testing.T) {
		t.Parallel()

		badCfg := config.Default()
		badCfg.Accounts.Routing = map[string]string{"@chat": "ghost"}

		_, err := planInvocation(t.Context(), newTestApp(accountsDir, badCfg), newFlaggedCmd(""), dlRunFlags{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"ghost"`)
	})

	t.Run("premium preferred finds local premium session", func(t *testing.T) {
		t.Parallel()

		extras, err := planInvocation(t.Context(), newTestApp(accountsDir, premiumCfg), newFlaggedCmd(""), dlRunFlags{}, nil)
		require.NoError(t, err)
		require.NotNil(t, extras)
		assert.Equal(t, "prem", extras.premiumAcct)
		assert.Nil(t, extras.routing)
	})

	t.Run("premium preferred without a premium session stays inert", func(t *testing.T) {
		t.Parallel()

		inertCfg := config.Default()
		inertCfg.Accounts.PremiumPreferred = true

		dir := t.TempDir()
		writeFakeSession(t, dir, "default")
		writeFakePremium(t, dir, "default", false)

		extras, err := planInvocation(t.Context(), newTestApp(dir, inertCfg), newFlaggedCmd(""), dlRunFlags{}, nil)
		require.NoError(t, err)
		require.NotNil(t, extras)
		assert.Empty(t, extras.premiumAcct, "no local premium session means no deferral")
	})
}

func TestPremiumDeferralCap(t *testing.T) {
	t.Parallel()

	const gib = 1 << 30

	assert.EqualValues(t, 2*gib, premiumDeferralCap(0, false), "non-premium session off takeout caps at the base limit")
	assert.EqualValues(t, 4*gib, premiumDeferralCap(0, true), "premium session off takeout caps at the premium limit")
	assert.EqualValues(t, 2*gib, premiumDeferralCap(2*gib, true), "an active takeout cap always wins")
}

func TestPartitionPremiumDeferred(t *testing.T) {
	t.Parallel()

	const gib = 1 << 30

	sized := func(gibs int64) *int64 {
		bytes := gibs * gib
		return &bytes
	}

	items := []store.MediaItem{
		{ChatID: 1, Size: sized(1)},
		{ChatID: 2, Size: sized(3)},
		{ChatID: 3, Size: sized(5)},
		{ChatID: 4, Size: nil},
	}

	main, deferred := partitionPremiumDeferred(items, 2*gib)
	require.Len(t, main, 3)
	require.Len(t, deferred, 1)
	assert.Equal(t, int64(2), deferred[0].ChatID, "only 2GiB < size <= 4GiB items defer")
	assert.Equal(t, int64(3), main[1].ChatID, "items beyond the premium cap stay in the main pass")
	assert.Equal(t, int64(4), main[2].ChatID, "unknown-size items stay in the main pass")

	main, deferred = partitionPremiumDeferred(items, 4*gib)
	assert.Empty(t, deferred, "nothing within a premium session's cap defers")
	assert.Len(t, main, 4)
}

func TestDeferredPremiumSpecs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"3", "7"}, deferredPremiumSpecs([]int64{7, 3, 3}),
		"chat ids render as numeric specs, deduped and sorted")
}

func TestDeferOversizedNoOpKeepsFatalBehavior(t *testing.T) {
	t.Parallel()

	const gib = 1 << 30

	size := int64(3 * gib)
	items := []store.MediaItem{{ChatID: 2, Size: &size}}

	cmd, _ := newOutCmd()
	app := &App{errStyle: NewStyler(false)}

	// No plan at all (premium_preferred off) or no local premium session:
	// the partition is a no-op and the items stay for the main pass, where
	// the oversized pre-check fails them with the cap reason.
	kept, err := deferOversizedToPremium(cmd, app, nil, false, 2*gib, items)
	require.NoError(t, err)
	assert.Equal(t, items, kept)

	kept, err = deferOversizedToPremium(cmd, app, &invocationExtras{}, false, 2*gib, items)
	require.NoError(t, err)
	assert.Equal(t, items, kept)
}

func TestRoutingAndPremiumPassHelpers(t *testing.T) {
	t.Parallel()

	extras := &invocationExtras{
		routed:      map[string][]string{"alpha": {"@a"}},
		deferrals:   []int64{5},
		premiumAcct: "prem",
	}

	assert.Len(t, routingPasses(extras), 1)
	assert.Empty(t, routingPasses(nil))

	passes := premiumPasses(extras)
	require.Len(t, passes, 1)
	assert.Equal(t, passPremium, passes[0].kind)

	assert.Empty(t, premiumPasses(&invocationExtras{deferrals: []int64{5}}),
		"no premium account means no premium pass")
	assert.Empty(t, premiumPasses(&invocationExtras{premiumAcct: "prem"}),
		"no deferrals means no premium pass")

	assert.Len(t, deferredPasses(extras), 2, "deferredPasses composes both helpers in order")
}

func TestDeferredPassesOrder(t *testing.T) {
	t.Parallel()

	extras := &invocationExtras{
		routed:      map[string][]string{"zeta": {"@c"}, "alpha": {"@a", "@b"}},
		deferrals:   []int64{9, 5},
		premiumAcct: "prem",
	}

	passes := deferredPasses(extras)
	require.Len(t, passes, 3)

	assert.Equal(t, passRouting, passes[0].kind)
	assert.Equal(t, "alpha", passes[0].account)
	assert.Equal(t, []string{"@a", "@b"}, passes[0].specs)

	assert.Equal(t, passRouting, passes[1].kind)
	assert.Equal(t, "zeta", passes[1].account)

	assert.Equal(t, passPremium, passes[2].kind)
	assert.Equal(t, "prem", passes[2].account)
	assert.Equal(t, []string{"5", "9"}, passes[2].specs)
}

// sessionCall records one fake account-session open.
type sessionCall struct {
	account string
	auth    string
}

func TestRunDeferredSessionsSequence(t *testing.T) {
	// No t.Parallel: overrides the package session-runner seam.

	var calls []sessionCall

	previous := accountSessionRunner
	accountSessionRunner = func(
		_ context.Context, account string, _ tg.Creds, cfg *config.Config, _ *config.Paths,
		_ func(context.Context, *telegram.Client) error,
	) error {
		calls = append(calls, sessionCall{account: account, auth: cfg.Auth.Account})

		return nil
	}

	t.Cleanup(func() { accountSessionRunner = previous })

	cmd := &cobra.Command{}

	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })

	cmd.SetOut(sink)
	cmd.SetErr(sink)

	cfg := config.Default()

	app := &App{cfg: cfg, errStyle: NewStyler(false)}

	extras := &invocationExtras{
		routed:      map[string][]string{"spare": {"@bar", "@baz"}},
		deferrals:   []int64{5},
		premiumAcct: "prem",
	}

	require.NoError(t, runDeferredSessions(cmd, app, tg.Creds{}, extras,
		"", filters.Options{}, &filters.Plan{}, runMode{}, dlRunFlags{}))

	require.Len(t, calls, 2)
	assert.Equal(t, "spare", calls[0].account)
	assert.Equal(t, "spare", calls[0].auth, "the session config must pin the routed account")
	assert.Equal(t, "prem", calls[1].account)
	assert.Equal(t, "prem", calls[1].auth, "the session config must pin the premium account")
}
