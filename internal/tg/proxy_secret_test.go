package tg_test

import (
	"encoding/hex"
	"testing"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/mtproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bareSecretHex is a 16-byte MTProxy secret in hex.
const bareSecretHex = "0123456789abcdef0123456789abcdef"

// tagDD and tagEE are the documented MTProxy secret tags: dd selects the
// padded-intermediate (secured) transport, ee fake TLS with a cloak domain
// appended after the 16 secret bytes.
const (
	tagDD = 0xdd
	tagEE = 0xee
)

func hexSecret(t *testing.T, raw string) []byte {
	t.Helper()

	secret, err := hex.DecodeString(raw)
	require.NoError(t, err)

	return secret
}

// TestMTProxySecretClassification asserts gotd's mtproxy.ParseSecret
// semantics, which ParseProxyURL relies on: a bare 16-byte secret selects
// the simple transport, a leading dd byte the padded (secured) transport,
// and a leading ee byte plus trailing domain bytes fake TLS with the cloak
// host carried after the secret.
func TestMTProxySecretClassification(t *testing.T) {
	t.Parallel()

	simple, err := mtproxy.ParseSecret(hexSecret(t, bareSecretHex))
	require.NoError(t, err)
	assert.Equal(t, mtproxy.Simple, simple.Type)
	assert.Equal(t, bareSecretHex, hex.EncodeToString(simple.Secret))
	assert.Empty(t, simple.CloakHost)

	secured, err := mtproxy.ParseSecret(hexSecret(t, "dd"+bareSecretHex))
	require.NoError(t, err)
	assert.Equal(t, mtproxy.Secured, secured.Type)
	assert.Equal(t, byte(tagDD), secured.Tag)
	assert.Equal(t, bareSecretHex, hex.EncodeToString(secured.Secret))

	domain := hex.EncodeToString([]byte(".example.com"))

	fakeTLS, err := mtproxy.ParseSecret(hexSecret(t, "ee"+bareSecretHex+domain))
	require.NoError(t, err)
	assert.Equal(t, mtproxy.TLS, fakeTLS.Type)
	assert.Equal(t, byte(tagEE), fakeTLS.Tag)
	assert.Equal(t, ".example.com", fakeTLS.CloakHost)
	assert.Equal(t, bareSecretHex, hex.EncodeToString(fakeTLS.Secret))

	// gotd keys off the length first: a 17-byte ee secret classifies as
	// padded-secured, and a dd-tagged secret longer than 17 bytes as fake
	// TLS carrying the cloak domain.
	paddedEE, err := mtproxy.ParseSecret(hexSecret(t, "ee"+bareSecretHex))
	require.NoError(t, err)
	assert.Equal(t, mtproxy.Secured, paddedEE.Type)

	tlsDD, err := mtproxy.ParseSecret(hexSecret(t, "dd"+bareSecretHex+domain))
	require.NoError(t, err)
	assert.Equal(t, mtproxy.TLS, tlsDD.Type)
	assert.Equal(t, ".example.com", tlsDD.CloakHost)

	for name, raw := range map[string]string{
		"too short":     bareSecretHex[:30],
		"unknown tag":   "aa" + bareSecretHex,
		"empty payload": "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := mtproxy.ParseSecret(hexSecret(t, raw))
			assert.Error(t, err, "secret %q must be rejected", raw)
		})
	}
}

// TestParseProxyURLMTProtoSecretWiring asserts ParseProxyURL accepts exactly
// the secret shapes mtproxy.ParseSecret classifies.
func TestParseProxyURLMTProtoSecretWiring(t *testing.T) {
	t.Parallel()

	domain := hex.EncodeToString([]byte("relay-cloak.example"))

	for _, raw := range []string{
		"mtproto://proxy.example:443/" + bareSecretHex,
		"mtproto://proxy.example:443/dd" + bareSecretHex,
		"mtproto://proxy.example:443/ee" + bareSecretHex + domain,
	} {
		resolver, err := tg.ParseProxyURL(raw)
		require.NoError(t, err, "url %q", raw)
		assert.NotNil(t, resolver, "url %q", raw)
	}
}
