package tg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountManagerInvalidNameEdges(t *testing.T) {
	t.Parallel()

	manager := tg.NewAccountManager(t.TempDir())

	_, err := manager.Dir("not a name")
	require.ErrorIs(t, err, tg.ErrAccountName)

	_, err = manager.Storage("not a name")
	require.ErrorIs(t, err, tg.ErrAccountName)

	_, err = manager.Device("not a name")
	require.ErrorIs(t, err, tg.ErrAccountName)

	err = manager.Delete("not a name")
	require.ErrorIs(t, err, tg.ErrAccountName)
}

func TestAccountManagerFileAsRootFails(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "file-root")
	require.NoError(t, os.WriteFile(root, nil, 0o600))

	manager := tg.NewAccountManager(root)

	_, err := manager.Storage("alpha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create account dir")

	_, err = manager.Device("alpha")
	require.Error(t, err)

	_, err = manager.List()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list accounts dir")
}

func TestDeleteFailsOnUndeletableContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager := tg.NewAccountManager(root)

	// A read-only subdirectory inside the account dir makes RemoveAll fail
	// while the root stays writable, so the lock can still be taken.
	inner := filepath.Join(root, "stuck", "inner")
	require.NoError(t, os.MkdirAll(inner, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(inner, "payload"), nil, 0o600))
	require.NoError(t, os.Chmod(inner, 0o500))

	t.Cleanup(func() {
		_ = os.Chmod(inner, 0o700)
	})

	err := manager.Delete("stuck")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove account dir")
}
