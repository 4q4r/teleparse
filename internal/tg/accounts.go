package tg

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
)

// Filesystem permissions for the account sandbox.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// archivedFolderID marks a dialog as archived server-side.
const archivedFolderID = 1

// namePattern restricts account names to a flat, safe namespace.
var namePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// AccountManager owns the multi-account sandbox under root: one directory per
// account holding session.json and device.json, with a sibling .lock file.
type AccountManager struct {
	root string
}

// NewAccountManager returns a manager rooted at dir (created lazily on first use).
func NewAccountManager(dir string) *AccountManager {
	return &AccountManager{root: dir}
}

// NormalizeName trims and lowercases name, then validates it against [a-z0-9_-].
func (m *AccountManager) NormalizeName(name string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if !namePattern.MatchString(canonical) {
		return "", fmt.Errorf("%q: %w", name, ErrAccountName)
	}

	return canonical, nil
}

// Dir returns the account directory, validating the name without touching disk.
func (m *AccountManager) Dir(name string) (string, error) {
	canonical, err := m.NormalizeName(name)
	if err != nil {
		return "", err
	}

	return filepath.Join(m.root, canonical), nil
}

// Lock takes the exclusive process-wide lock for name (LOCK_EX|LOCK_NB).
// The lock is held until Close; a second attempt fails with ErrAccountInUse.
func (m *AccountManager) Lock(name string) (*AccountLock, error) {
	canonical, err := m.NormalizeName(name)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(m.root, dirPerm); err != nil {
		return nil, fmt.Errorf("create accounts dir %s: %w", m.root, err)
	}

	path := filepath.Join(m.root, canonical+".lock")

	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	if err := lockHandle(handle.Fd()); err != nil {
		_ = handle.Close()

		return nil, fmt.Errorf("%q: %w: %w", canonical, ErrAccountInUse, err)
	}

	return &AccountLock{handle: handle}, nil
}

// AccountLock is a held flock on an account's lockfile.
type AccountLock struct {
	handle *os.File
}

// Close releases the lock and closes the file descriptor.
func (l *AccountLock) Close() error {
	if err := unlockHandle(l.handle.Fd()); err != nil {
		_ = l.handle.Close()

		return fmt.Errorf("unlock account: %w", err)
	}

	if err := l.handle.Close(); err != nil {
		return fmt.Errorf("close lockfile: %w", err)
	}

	return nil
}

// Storage returns the session file storage for name, creating the account
// directory with 0700. Session files are written by gotd with 0600.
func (m *AccountManager) Storage(name string) (*session.FileStorage, error) {
	dir, err := m.Dir(name)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create account dir %s: %w", dir, err)
	}

	return &session.FileStorage{Path: filepath.Join(dir, "session.json")}, nil
}

// Device returns the stable device identity for name. The profile is chosen
// deterministically from sha256(name) on first use, persisted to device.json
// and reused verbatim afterwards so the fingerprint never drifts.
func (m *AccountManager) Device(name string) (telegram.DeviceConfig, error) {
	dir, err := m.Dir(name)
	if err != nil {
		return telegram.DeviceConfig{}, err
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return telegram.DeviceConfig{}, fmt.Errorf("create account dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, "device.json")

	raw, err := os.ReadFile(path)
	if err == nil {
		var stored deviceProfile
		if err := json.Unmarshal(raw, &stored); err != nil {
			return telegram.DeviceConfig{}, fmt.Errorf("%s: %w: %w", path, ErrDeviceCorrupt, err)
		}

		return stored.config(), nil
	}

	if !os.IsNotExist(err) {
		return telegram.DeviceConfig{}, fmt.Errorf("read device profile %s: %w", path, err)
	}

	fresh := pickDeviceProfile(name)

	blob, err := json.MarshalIndent(fresh, "", "  ")
	if err != nil {
		return telegram.DeviceConfig{}, fmt.Errorf("encode device profile: %w", err)
	}

	blob = append(blob, '\n')

	if err := os.WriteFile(path, blob, filePerm); err != nil {
		return telegram.DeviceConfig{}, fmt.Errorf("write device profile %s: %w", path, err)
	}

	return fresh.config(), nil
}

// Delete removes the account directory and lockfile. It fails with
// ErrAccountInUse when another process holds the account lock.
func (m *AccountManager) Delete(name string) error {
	dir, err := m.Dir(name)
	if err != nil {
		return err
	}

	lock, err := m.Lock(name)
	if err != nil {
		return err
	}

	unlock := lock.Close

	if err := os.RemoveAll(dir); err != nil {
		_ = unlock()

		return fmt.Errorf("remove account dir %s: %w", dir, err)
	}

	canonical := filepath.Base(dir)

	if err := os.Remove(filepath.Join(m.root, canonical+".lock")); err != nil && !os.IsNotExist(err) {
		_ = unlock()

		return fmt.Errorf("remove lockfile: %w", err)
	}

	return unlock()
}

// List returns the sorted names of accounts that have a session file.
func (m *AccountManager) List() ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("list accounts dir %s: %w", m.root, err)
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionPath := filepath.Join(m.root, entry.Name(), "session.json")
		if _, err := os.Stat(sessionPath); err != nil {
			continue
		}

		names = append(names, entry.Name())
	}

	return names, nil
}

// deviceProfile is the persisted per-account device identity.
type deviceProfile struct {
	DeviceModel    string `json:"device_model"`
	SystemVersion  string `json:"system_version"`
	AppVersion     string `json:"app_version"`
	SystemLangCode string `json:"system_lang_code"`
	LangPack       string `json:"lang_pack"`
	LangCode       string `json:"lang_code"`
}

func (p deviceProfile) config() telegram.DeviceConfig {
	return telegram.DeviceConfig{
		DeviceModel:    p.DeviceModel,
		SystemVersion:  p.SystemVersion,
		AppVersion:     p.AppVersion,
		SystemLangCode: p.SystemLangCode,
		LangPack:       p.LangPack,
		LangCode:       p.LangCode,
	}
}

// deviceProfiles returns realistic mainstream client fingerprints. Returned
// fresh on every call: no package-level mutable state.
func deviceProfiles() []deviceProfile {
	return []deviceProfile{
		{
			DeviceModel:    "Samsung SM-G991B",
			SystemVersion:  "SDK 33",
			AppVersion:     "10.3.0 (2742)",
			SystemLangCode: "en",
			LangPack:       "android",
			LangCode:       "en",
		},
		{
			DeviceModel:    "Xiaomi 2201123G",
			SystemVersion:  "SDK 31",
			AppVersion:     "10.3.0 (2742)",
			SystemLangCode: "en",
			LangPack:       "android",
			LangCode:       "en",
		},
		{
			DeviceModel:    "iPhone 14 Pro Max",
			SystemVersion:  "iOS 16.6 20G75",
			AppVersion:     "10.3.0",
			SystemLangCode: "en",
			LangPack:       "ios",
			LangCode:       "en",
		},
		{
			DeviceModel:    "PC 64bit",
			SystemVersion:  "Windows 10 x64",
			AppVersion:     "4.14.7 x64",
			SystemLangCode: "en",
			LangPack:       "tdesktop",
			LangCode:       "en",
		},
		{
			DeviceModel:    "PC 64bit",
			SystemVersion:  "Ubuntu 22.04 x64",
			AppVersion:     "4.14.7 x64",
			SystemLangCode: "en",
			LangPack:       "tdesktop",
			LangCode:       "en",
		},
		{
			DeviceModel:    "HUAWEI ELS-NX9",
			SystemVersion:  "SDK 32",
			AppVersion:     "10.3.0 (2742)",
			SystemLangCode: "en",
			LangPack:       "android",
			LangCode:       "en",
		},
	}
}

// pickDeviceProfile deterministically maps an account name to a profile so the
// same name always yields the same device fingerprint.
func pickDeviceProfile(name string) deviceProfile {
	profiles := deviceProfiles()

	digest := sha256.Sum256([]byte(name))
	index := int(digest[0]) % len(profiles)

	return profiles[index]
}
