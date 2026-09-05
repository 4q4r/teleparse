package tg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()

	manager := tg.NewAccountManager(t.TempDir())

	trimmed, err := manager.NormalizeName("  alt-1  ")
	require.NoError(t, err)
	assert.Equal(t, "alt-1", trimmed)

	valid := map[string]string{
		"default":   "default",
		"Work":      "work",
		"a_b-c":     "a_b-c",
		"000":       "000",
		"with-dash": "with-dash",
	}
	for input, want := range valid {
		got, err := manager.NormalizeName(input)
		require.NoError(t, err, "input %q", input)
		assert.Equal(t, want, got, "input %q", input)
	}

	invalid := []string{"", " ", "bad name", "UPPER!", "кириллица", "sla/sh", "co:lon"}
	for _, input := range invalid {
		_, err := manager.NormalizeName(input)
		assert.ErrorIs(t, err, tg.ErrAccountName, "input %q", input)
	}
}

func TestDeviceDeterministic(t *testing.T) {
	t.Parallel()

	dirOne := t.TempDir()
	dirTwo := t.TempDir()

	first, err := tg.NewAccountManager(dirOne).Device("main")
	require.NoError(t, err)

	second, err := tg.NewAccountManager(dirTwo).Device("main")
	require.NoError(t, err)
	assert.Equal(t, first, second)

	assert.FileExists(t, filepath.Join(dirOne, "main", "device.json"))

	info, err := os.Stat(filepath.Join(dirOne, "main", "device.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestDeviceCoversMultipleProfiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := tg.NewAccountManager(root)

	models := map[string]bool{}

	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		device, err := manager.Device(name)
		require.NoError(t, err, "name %q", name)
		models[device.DeviceModel] = true
	}

	assert.Greater(t, len(models), 1, "different names should reach different device profiles")

	first, err := manager.Device("a")
	require.NoError(t, err)

	again, err := tg.NewAccountManager(t.TempDir()).Device("a")
	require.NoError(t, err)
	assert.Equal(t, first, again)
}

func TestDeviceReusesPersistedProfile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := tg.NewAccountManager(root)

	created, err := manager.Device("acct")
	require.NoError(t, err)
	require.NotEmpty(t, created.DeviceModel)

	raw := `{
		"device_model": "Custom Device",
		"system_version": "Custom OS 1",
		"app_version": "Custom App 1",
		"system_lang_code": "de",
		"lang_pack": "custom",
		"lang_code": "de"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(root, "acct", "device.json"), []byte(raw), 0o600))

	loaded, err := manager.Device("acct")
	require.NoError(t, err)
	assert.Equal(t, "Custom Device", loaded.DeviceModel)
	assert.Equal(t, "Custom OS 1", loaded.SystemVersion)
	assert.Equal(t, "Custom App 1", loaded.AppVersion)
	assert.Equal(t, "de", loaded.SystemLangCode)
	assert.Equal(t, "custom", loaded.LangPack)
	assert.Equal(t, "de", loaded.LangCode)
}

func TestDeviceRejectsCorruptProfile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "acct"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "acct", "device.json"), []byte("{not json"), 0o600))

	_, err := tg.NewAccountManager(root).Device("acct")
	assert.Error(t, err)
}

func TestLockExclusive(t *testing.T) {
	t.Parallel()

	manager := tg.NewAccountManager(t.TempDir())

	lock, err := manager.Lock("busy")
	require.NoError(t, err)

	_, err = manager.Lock("busy")
	require.ErrorIs(t, err, tg.ErrAccountInUse)

	_, err = manager.Lock("free")
	require.NoError(t, err)

	require.NoError(t, lock.Close())

	relocked, err := manager.Lock("busy")
	require.NoError(t, err)
	require.NoError(t, relocked.Close())
}

func TestStorageCreatesSandbox(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := tg.NewAccountManager(root)

	storage, err := manager.Storage("acc")
	require.NoError(t, err)
	require.NotNil(t, storage)
	assert.Equal(t, filepath.Join(root, "acc", "session.json"), storage.Path)

	info, err := os.Stat(filepath.Join(root, "acc"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestListAccounts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name, "session.json"), []byte("{}"), 0o600))
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, "locked-out"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray.lock"), nil, 0o600))

	manager := tg.NewAccountManager(root)
	names, err := manager.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names)
}

func TestDeleteRemovesAccountDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := tg.NewAccountManager(root)

	storage, err := manager.Storage("gone")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(storage.Path, []byte("{}"), 0o600))

	require.NoError(t, manager.Delete("gone"))
	assert.NoDirExists(t, filepath.Join(root, "gone"))
	assert.NoFileExists(t, filepath.Join(root, "gone.lock"))

	_, err = manager.List()
	require.NoError(t, err)

	var names []string
	assert.NotContains(t, names, "gone")
}

func TestDeleteFailsWhenInUse(t *testing.T) {
	t.Parallel()

	manager := tg.NewAccountManager(t.TempDir())

	lock, err := manager.Lock("busy")
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })

	err = manager.Delete("busy")
	require.ErrorIs(t, err, tg.ErrAccountInUse)
}

func TestDeviceConfigShape(t *testing.T) {
	t.Parallel()

	device, err := tg.NewAccountManager(t.TempDir()).Device("shape")
	require.NoError(t, err)

	assert.NotEmpty(t, device.DeviceModel)
	assert.NotEmpty(t, device.SystemVersion)
	assert.NotEmpty(t, device.AppVersion)
	assert.NotEmpty(t, device.SystemLangCode)
	assert.NotEmpty(t, device.LangCode)

	var zero telegram.DeviceConfig
	assert.NotEqual(t, zero, device)
}
