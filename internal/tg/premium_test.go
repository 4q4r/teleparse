package tg_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/tg"

	gotdtg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPremiumManager builds an AccountManager over a temp accounts dir.
func newPremiumManager(t *testing.T) (*tg.AccountManager, string) {
	t.Helper()

	dir := t.TempDir()

	return tg.NewAccountManager(dir), dir
}

// premiumUser builds a self user with the given Premium flag.
func premiumUser(premium bool) *gotdtg.User {
	user := &gotdtg.User{Username: "tester"}
	user.SetPremium(premium)

	return user
}

// writePremiumCache persists a premium cache record with an arbitrary age so
// TTL boundaries are testable without fake clocks.
func writePremiumCache(t *testing.T, accountsDir, name string, premium bool, age time.Duration) {
	t.Helper()

	dir := filepath.Join(accountsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	blob := `{"premium": ` + boolLiteral(premium) + `, "checked_at": "` +
		time.Now().Add(-age).UTC().Format(time.RFC3339Nano) + `"}` + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "premium.json"), []byte(blob), 0o600))
}

func boolLiteral(value bool) string {
	if value {
		return "true"
	}

	return "false"
}

// readPremiumCache reports the cached premium flag and whether the stamp exists.
func readPremiumCache(t *testing.T, accountsDir, name string) (bool, bool) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(accountsDir, name, "premium.json"))
	if err != nil {
		return false, false
	}

	var record struct {
		Premium   bool      `json:"premium"`
		CheckedAt time.Time `json:"checked_at"`
	}

	require.NoError(t, json.Unmarshal(raw, &record))

	return record.Premium, !record.CheckedAt.IsZero()
}

func TestAccountPremiumQueriesAndCaches(t *testing.T) {
	t.Parallel()

	manager, accountsDir := newPremiumManager(t)

	calls := 0

	self := func(context.Context) (*gotdtg.User, error) {
		calls++

		return premiumUser(true), nil
	}

	status := manager.AccountPremium(t.Context(), "default", self)
	assert.Equal(t, tg.PremiumStatus{Premium: true, Source: tg.PremiumSourceQuery}, status)

	cached, stamped := readPremiumCache(t, accountsDir, "default")
	assert.True(t, cached, "query result must be persisted")
	assert.True(t, stamped)

	// A fresh cache hit answers without a second query.
	again := manager.AccountPremium(t.Context(), "default", self)
	assert.Equal(t, tg.PremiumStatus{Premium: true, Source: tg.PremiumSourceCache}, again)
	assert.Equal(t, 1, calls, "cache hit must skip the query")
}

func TestAccountPremiumStaleCacheRequeries(t *testing.T) {
	t.Parallel()

	manager, accountsDir := newPremiumManager(t)
	writePremiumCache(t, accountsDir, "default", false, 25*time.Hour)

	self := func(context.Context) (*gotdtg.User, error) { return premiumUser(true), nil }

	status := manager.AccountPremium(t.Context(), "default", self)
	assert.Equal(t, tg.PremiumStatus{Premium: true, Source: tg.PremiumSourceQuery}, status)

	cached, _ := readPremiumCache(t, accountsDir, "default")
	assert.True(t, cached, "cache must be refreshed with the new answer")
}

func TestAccountPremiumQueryFailureFallsBackToStaleCache(t *testing.T) {
	t.Parallel()

	manager, accountsDir := newPremiumManager(t)
	writePremiumCache(t, accountsDir, "default", true, 25*time.Hour)

	self := func(context.Context) (*gotdtg.User, error) { return nil, errors.New("network down") }

	status := manager.AccountPremium(t.Context(), "default", self)
	assert.Equal(t, tg.PremiumStatus{Premium: true, Source: tg.PremiumSourceCache}, status)
}

func TestAccountPremiumQueryFailureWithoutCache(t *testing.T) {
	t.Parallel()

	manager, _ := newPremiumManager(t)

	self := func(context.Context) (*gotdtg.User, error) { return nil, errors.New("network down") }

	status := manager.AccountPremium(t.Context(), "default", self)
	assert.Equal(t, tg.PremiumStatus{Premium: false, Source: tg.PremiumSourceNone}, status)
}

func TestAccountPremiumNilSelfResolvesFromCache(t *testing.T) {
	t.Parallel()

	manager, accountsDir := newPremiumManager(t)
	writePremiumCache(t, accountsDir, "work", true, time.Hour)

	status := manager.AccountPremium(t.Context(), "work", nil)
	assert.Equal(t, tg.PremiumStatus{Premium: true, Source: tg.PremiumSourceCache}, status)
}

func TestAccountPremiumNilSelfWithoutCache(t *testing.T) {
	t.Parallel()

	manager, _ := newPremiumManager(t)

	status := manager.AccountPremium(t.Context(), "default", nil)
	assert.Equal(t, tg.PremiumStatus{Premium: false, Source: tg.PremiumSourceNone}, status)
}

func TestAccountPremiumCorruptCacheFallsThroughToQuery(t *testing.T) {
	t.Parallel()

	manager, accountsDir := newPremiumManager(t)

	dir := filepath.Join(accountsDir, "default")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "premium.json"), []byte("{not json"), 0o600))

	self := func(context.Context) (*gotdtg.User, error) { return premiumUser(false), nil }

	status := manager.AccountPremium(t.Context(), "default", self)
	assert.Equal(t, tg.PremiumStatus{Premium: false, Source: tg.PremiumSourceQuery}, status)
}

func TestAccountPremiumRejectsInvalidName(t *testing.T) {
	t.Parallel()

	manager, _ := newPremiumManager(t)

	self := func(context.Context) (*gotdtg.User, error) { return premiumUser(true), nil }

	status := manager.AccountPremium(t.Context(), "BAD NAME", self)
	assert.Equal(t, tg.PremiumStatus{Premium: false, Source: tg.PremiumSourceNone}, status)
}
