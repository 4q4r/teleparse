package tg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
)

// TDesktopSessionImport reads a Telegram Desktop tdata directory, asks which
// account to take when several are stored, converts it to a gotd session and
// persists it into storage — the same session file phone and QR logins use.
//
// Limitations: passcode-protected tdata archives (local lock code) are not
// supported, and only the auth key travels over; peer caches rebuild lazily.
func TDesktopSessionImport(ctx context.Context, dir string, ask Prompter, storage *session.FileStorage) error {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: %w", dir, ErrTDesktopMissing)
		}

		return fmt.Errorf("stat %s: %w", dir, err)
	}

	accounts, err := tdesktop.Read(dir, nil)
	if err != nil {
		return fmt.Errorf("read tdata %s: %w", dir, err)
	}

	if len(accounts) == 0 {
		return fmt.Errorf("%s: %w", dir, ErrTDesktopEmpty)
	}

	which, err := pickTDesktopAccount(accounts, ask)
	if err != nil {
		return err
	}

	data, err := session.TDesktopSession(accounts[which])
	if err != nil {
		return fmt.Errorf("convert tdata account: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(storage.Path), dirPerm); err != nil {
		return fmt.Errorf("create account dir: %w", err)
	}

	if err := (&session.Loader{Storage: storage}).Save(ctx, data); err != nil {
		return fmt.Errorf("store session: %w", err)
	}

	return nil
}

// pickTDesktopAccount returns the index of the account to import: silently
// the only one, or the user's 1-based choice after listing every stored user.
func pickTDesktopAccount(accounts []tdesktop.Account, ask Prompter) (int, error) {
	if len(accounts) == 1 {
		return 0, nil
	}

	for {
		var list strings.Builder

		for idx, acc := range accounts {
			fmt.Fprintf(&list, "\n  %d) user %d (main DC %d)", idx+1, acc.Authorization.UserID, acc.Authorization.MainDC)
		}

		answer, err := ask.Line("Multiple accounts found in tdata; pick one" + list.String())
		if err != nil {
			return 0, fmt.Errorf("read account choice: %w", err)
		}

		which, convErr := strconv.Atoi(answer)
		if convErr == nil && which >= 1 && which <= len(accounts) {
			return which - 1, nil
		}
	}
}
