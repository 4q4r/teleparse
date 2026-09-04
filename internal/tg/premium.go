package tg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tgapi "github.com/gotd/td/tg"
)

// premiumCacheTTL bounds how long a cached premium answer is trusted before
// the next run re-queries the account.
const premiumCacheTTL = 24 * time.Hour

// premiumFileName is the per-account premium cache file, a sibling of
// session.json and device.json.
const premiumFileName = "premium.json"

// SelfFunc is the seam over telegram.Client.Self used to query the
// account's Premium flag; tests inject fakes.
type SelfFunc func(ctx context.Context) (*tgapi.User, error)

// PremiumSource names where a premium answer came from.
type PremiumSource string

// Premium answer sources.
const (
	PremiumSourceQuery PremiumSource = "query"
	PremiumSourceCache PremiumSource = "cache"
	PremiumSourceNone  PremiumSource = "none"
)

// PremiumStatus is the resolved Telegram Premium state of an account.
type PremiumStatus struct {
	Premium bool
	Source  PremiumSource
}

// premiumRecord is the persisted <account>/premium.json payload.
type premiumRecord struct {
	Premium   bool      `json:"premium"`
	CheckedAt time.Time `json:"checked_at"`
}

// AccountPremium resolves the account's Telegram Premium flag. A cache hit
// inside premiumCacheTTL answers without a query; otherwise self is called
// and the answer persisted. Query failures fall back to a stale cache and,
// absent one, to a non-premium answer: detection never blocks downloads. A
// nil self resolves from the cache alone.
func (m *AccountManager) AccountPremium(ctx context.Context, name string, self SelfFunc) PremiumStatus {
	if _, err := m.Dir(name); err != nil {
		return PremiumStatus{Premium: false, Source: PremiumSourceNone}
	}

	cached, ok := m.readPremium(name)

	if ok && time.Since(cached.CheckedAt) < premiumCacheTTL {
		return PremiumStatus{Premium: cached.Premium, Source: PremiumSourceCache}
	}

	if self == nil {
		return premiumFallback(cached, ok)
	}

	user, err := self(ctx)
	if err != nil {
		return premiumFallback(cached, ok)
	}

	record := premiumRecord{Premium: user.Premium, CheckedAt: time.Now()}

	_ = m.writePremium(name, record)

	// Persisting is best-effort; the queried answer stands either way.
	return PremiumStatus{Premium: record.Premium, Source: PremiumSourceQuery}
}

// premiumFallback answers from the (possibly stale) cache when no fresh
// query result is available.
func premiumFallback(cached premiumRecord, ok bool) PremiumStatus {
	if ok {
		return PremiumStatus{Premium: cached.Premium, Source: PremiumSourceCache}
	}

	return PremiumStatus{Premium: false, Source: PremiumSourceNone}
}

// readPremium loads the cache record, reporting false when absent, corrupt
// or held under an invalid account name; all three cases re-query.
func (m *AccountManager) readPremium(name string) (premiumRecord, bool) {
	dir, err := m.Dir(name)
	if err != nil {
		return premiumRecord{}, false
	}

	raw, err := os.ReadFile(filepath.Join(dir, premiumFileName))
	if err != nil {
		return premiumRecord{}, false
	}

	var record premiumRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return premiumRecord{}, false
	}

	return record, true
}

// writePremium persists the cache record next to the session.
func (m *AccountManager) writePremium(name string, record premiumRecord) error {
	dir, err := m.Dir(name)
	if err != nil {
		return fmt.Errorf("resolve account dir: %w", err)
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("create account dir %s: %w", dir, err)
	}

	blob, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode premium record: %w", err)
	}

	blob = append(blob, '\n')

	if err := os.WriteFile(filepath.Join(dir, premiumFileName), blob, filePerm); err != nil {
		return fmt.Errorf("write premium record: %w", err)
	}

	return nil
}
