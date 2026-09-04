package tg_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"teleparse/internal/tg"
	"testing"

	"github.com/gotd/td/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func createTelethonSession(t *testing.T, path string, authKey []byte) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(context.Background(), `CREATE TABLE sessions (
		dc_id integer PRIMARY KEY,
		server_address text,
		port integer,
		auth_key blob
	)`)
	require.NoError(t, err)

	_, err = db.ExecContext(context.Background(), "INSERT INTO sessions (dc_id, server_address, port, auth_key) VALUES (?, ?, ?, ?)",
		2, "149.154.167.50", 443, authKey)
	require.NoError(t, err)
}

func TestTelethonSessionImportOK(t *testing.T) {
	t.Parallel()

	authKey := make([]byte, 256)
	_, err := rand.Read(authKey)
	require.NoError(t, err)

	dir := t.TempDir()
	source := filepath.Join(dir, "telethon.session")
	createTelethonSession(t, source, authKey)

	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	ctx := context.Background()
	require.NoError(t, tg.TelethonSessionImport(ctx, source, storage))

	loader := &session.Loader{Storage: storage}
	data, err := loader.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, data.DC)
	assert.Equal(t, "149.154.167.50:443", data.Addr)
	assert.Equal(t, authKey, data.AuthKey)
	assert.Len(t, data.AuthKeyID, 8)
}

func TestTelethonSessionImportMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	err := tg.TelethonSessionImport(context.Background(), filepath.Join(dir, "nope.session"), storage)
	assert.ErrorIs(t, err, tg.ErrTelethonMissing)
}

func TestTelethonSessionImportBadSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "empty.session")

	db, err := sql.Open("sqlite", source)
	require.NoError(t, err)
	require.NoError(t, db.PingContext(context.Background()))
	require.NoError(t, db.Close())

	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	err = tg.TelethonSessionImport(context.Background(), source, storage)
	assert.ErrorIs(t, err, tg.ErrTelethonSchema)
}

func TestTelethonSessionImportNoRow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "norows.session")

	db, err := sql.Open("sqlite", source)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(context.Background(), "CREATE TABLE sessions (dc_id integer, server_address text, port integer, auth_key blob)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	err = tg.TelethonSessionImport(context.Background(), source, storage)
	assert.ErrorIs(t, err, tg.ErrTelethonSchema)
}

func TestTelethonSessionImportBadKeyLength(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "shortkey.session")
	createTelethonSession(t, source, []byte("too-short"))

	storage := &session.FileStorage{Path: filepath.Join(dir, "acct", "session.json")}

	err := tg.TelethonSessionImport(context.Background(), source, storage)
	assert.ErrorIs(t, err, tg.ErrTelethonSchema)
}
