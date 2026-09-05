package tg

import (
	"path/filepath"
	"testing"

	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tdesktopAccounts builds N in-memory tdata accounts with distinct user IDs.
func tdesktopAccounts(howMany int) []tdesktop.Account {
	accounts := make([]tdesktop.Account, 0, howMany)

	for idx := range howMany {
		accounts = append(accounts, tdesktop.Account{
			IDx: uint32(idx),
			Authorization: tdesktop.MTPAuthorization{
				UserID: uint64(1000 + idx),
				MainDC: 2,
			},
		})
	}

	return accounts
}

func TestPickTDesktopAccountSingleNeedsNoPrompt(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{}

	idx, err := pickTDesktopAccount(tdesktopAccounts(1), ask)
	require.NoError(t, err)
	assert.Equal(t, 0, idx)
	assert.Empty(t, ask.asked, "a single account must not prompt")
}

func TestPickTDesktopAccountListsEveryUser(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{lines: []string{"2"}}

	idx, err := pickTDesktopAccount(tdesktopAccounts(3), ask)
	require.NoError(t, err)
	assert.Equal(t, 1, idx)
	require.Len(t, ask.asked, 1)

	prompt := ask.asked[0]
	assert.Contains(t, prompt, "1000")
	assert.Contains(t, prompt, "1001")
	assert.Contains(t, prompt, "1002")
}

func TestPickTDesktopAccountReasksOnGarbage(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{lines: []string{"nope", "9", "1"}}

	idx, err := pickTDesktopAccount(tdesktopAccounts(2), ask)
	require.NoError(t, err)
	assert.Equal(t, 0, idx)
	require.Len(t, ask.asked, 3, "two invalid answers must be re-asked")
}

func TestPickTDesktopAccountPrompterFailure(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{failAt: 1}

	_, err := pickTDesktopAccount(tdesktopAccounts(2), ask)
	require.ErrorContains(t, err, "read account choice")
}

func TestTDesktopSessionImportMissingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	err := TDesktopSessionImport(t.Context(), filepath.Join(dir, "absent"), &scriptPrompter{}, storage)
	require.ErrorIs(t, err, ErrTDesktopMissing)
}

func TestTDesktopSessionImportNotATdataDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	err := TDesktopSessionImport(t.Context(), dir, &scriptPrompter{}, storage)
	require.Error(t, err, "an empty directory is not a tdata folder")
	assert.NotErrorIs(t, err, ErrTDesktopMissing)
}
