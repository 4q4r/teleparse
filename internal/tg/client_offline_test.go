package tg_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram/dcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbeProxyRejectsBadURLs covers the offline validation path: a bad
// proxy URL fails before any dial is attempted.
func TestProbeProxyRejectsBadURLs(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"quic://host:443", "mtproto://host:443/zz"} {
		_, err := tg.ProbeProxy(t.Context(), raw)
		require.Error(t, err, "url %q", raw)
	}
}

// TestSOCKS5DialRunsThroughContextDialer dials a plain loopback listener
// through the socks5 dialer: the TCP connection is local-only, the SOCKS5
// handshake against the non-SOCKS5 listener fails, proving the dial function
// honors ctx and surfaces transport errors without leaving the machine.
func TestSOCKS5DialRunsThroughContextDialer(t *testing.T) {
	t.Parallel()

	listener := listenLocal(t)

	resolver, err := tg.ParseProxyURL("socks5://" + listener.Addr().String())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	conn, err := resolver.Primary(ctx, 2, dcs.Prod())
	require.Error(t, err, "a non-SOCKS5 listener must fail the handshake")
	assert.Nil(t, conn)
}

// TestLoginLogoutFailOfflineWithoutCreds covers the credential gate: Login
// and Logout refuse to start a client when the passed credentials are the
// zero pair, which is the first offline-reachable branch of the client.Run
// shell. The authorized paths need a live MTProto connection (see
// smoke_test.go for the documented boundary).
func TestLoginLogoutFailOfflineWithoutCreds(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	paths := &config.Paths{}

	err := tg.Login(t.Context(), "main", tg.Creds{}, "+15551234567", nil, cfg, paths)
	require.ErrorIs(t, err, tg.ErrCredsUnset)

	err = tg.Logout(t.Context(), "main", tg.Creds{}, cfg, paths)
	require.ErrorIs(t, err, tg.ErrCredsUnset)
}

// TestLoginRunSetupAbortsBeforeDialing covers Run's offline setup chain
// with credentials present: account storage, device profile, proxy resolver
// and the account lock are built before client.Run dials, and a pre-cancelled
// context ends the run before any packet leaves the machine (gotd treats
// ctx cancellation as a graceful stop and returns nil).
func TestLoginRunSetupAbortsBeforeDialing(t *testing.T) {
	t.Parallel()

	creds := tg.Creds{APIID: 123456, APIHash: "deadbeefcafe"}

	root := filepath.Join(t.TempDir(), "accounts")
	paths := &config.Paths{AccountsDir: root}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, tg.Login(ctx, "main", creds, "+15551234567", nil, &config.Config{}, paths))

	_, err := os.Stat(filepath.Join(root, "main", "device.json"))
	require.NoError(t, err, "the offline setup chain must run before any dial")
}
