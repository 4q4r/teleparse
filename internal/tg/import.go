package tg

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	_ "modernc.org/sqlite"
)

// Telethon auth keys are always 256 bytes.
const authKeySize = 256

// TelethonSessionImport reads a Telethon SQLite .session file (single-row
// `sessions` table: dc_id, server_address, port, auth_key) and stores the
// equivalent gotd session into storage.
//
// Limitation: only the auth key travels over; Telethon's entity cache
// (peer usernames/access hashes) is not imported and will be rebuilt lazily.
func TelethonSessionImport(ctx context.Context, path string, storage *session.FileStorage) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: %w", path, ErrTelethonMissing)
		}

		return fmt.Errorf("stat %s: %w", path, err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open %s: %w: %w", path, ErrTelethonSchema, err)
	}

	defer func() { _ = db.Close() }()

	row := db.QueryRowContext(ctx,
		"SELECT dc_id, server_address, port, auth_key FROM sessions LIMIT 1")

	var (
		dcID    int
		address string
		port    int
		authKey []byte
	)

	err = row.Scan(&dcID, &address, &port, &authKey)
	if err != nil {
		return fmt.Errorf("%s: %w: %w", path, ErrTelethonSchema, err)
	}

	if len(authKey) != authKeySize {
		return fmt.Errorf("%s: auth_key %d bytes: %w", path, len(authKey), ErrTelethonSchema)
	}

	if err := os.MkdirAll(filepath.Dir(storage.Path), dirPerm); err != nil {
		return fmt.Errorf("create account dir: %w", err)
	}

	var key crypto.Key

	copy(key[:], authKey)

	withID := key.WithID()

	data := &session.Data{
		DC:        dcID,
		Addr:      net.JoinHostPort(address, strconv.Itoa(port)),
		AuthKey:   key[:],
		AuthKeyID: withID.ID[:],
	}

	if err := (&session.Loader{Storage: storage}).Save(ctx, data); err != nil {
		return fmt.Errorf("store session: %w", err)
	}

	return nil
}
