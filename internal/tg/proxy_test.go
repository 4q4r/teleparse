package tg_test

import (
	"teleparse/internal/tg"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const plainSecretHex = "0123456789abcdef0123456789abcdef"

func TestParseProxyURLEmptyUsesDefault(t *testing.T) {
	t.Parallel()

	resolver, err := tg.ParseProxyURL("")
	require.NoError(t, err)
	assert.NotNil(t, resolver)
}

func TestParseProxyURLSocks5(t *testing.T) {
	t.Parallel()

	resolver, err := tg.ParseProxyURL("socks5://127.0.0.1:1080")
	require.NoError(t, err)
	assert.NotNil(t, resolver)

	withAuth, err := tg.ParseProxyURL("socks5://user:pass@10.0.0.1:9050")
	require.NoError(t, err)
	assert.NotNil(t, withAuth)
}

func TestParseProxyURLSocks4(t *testing.T) {
	t.Parallel()

	resolver, err := tg.ParseProxyURL("socks4://127.0.0.1:4144")
	require.NoError(t, err)
	assert.NotNil(t, resolver)
}

func TestParseProxyURLHTTP(t *testing.T) {
	t.Parallel()

	resolver, err := tg.ParseProxyURL("http://user:pass@proxy.local:3128")
	require.NoError(t, err)
	assert.NotNil(t, resolver)
}

func TestParseProxyURLMTProto(t *testing.T) {
	t.Parallel()

	cases := []string{
		"mtproto://proxy.example:443/" + plainSecretHex,
		"mtproto://proxy.example:443/dd" + plainSecretHex,
		"mtproto://proxy.example:443/ee" + plainSecretHex + "777777",
		"mtproto://proxy.example:443/ee" + plainSecretHex + "2e6578616d706c652e636f6d",
	}
	for _, raw := range cases {
		resolver, err := tg.ParseProxyURL(raw)
		require.NoError(t, err, "url %q", raw)
		assert.NotNil(t, resolver, "url %q", raw)
	}
}

func TestParseProxyURLMTProtoBadSecret(t *testing.T) {
	t.Parallel()

	cases := []string{
		"mtproto://proxy.example:443/xyz",                      // non-hex
		"mtproto://proxy.example:443/abcd",                     // too short
		"mtproto://proxy.example:443/" + plainSecretHex[:30],   // 15 bytes
		"mtproto://proxy.example:443/",                         // empty secret
		"mtproto://proxy.example:443/" + plainSecretHex + "aa", // 17 bytes unknown tag
	}
	for _, raw := range cases {
		_, err := tg.ParseProxyURL(raw)
		assert.ErrorIs(t, err, tg.ErrBadProxySecret, "url %q", raw)
	}
}

func TestParseProxyURLBadScheme(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"quic://host:443", "socks5h://host:1080", "ftp://host"} {
		_, err := tg.ParseProxyURL(raw)
		require.ErrorIs(t, err, tg.ErrBadProxyScheme, "url %q", raw)
	}
}

func TestParseProxyURLMalformed(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"socks5://", "http://[::1", "mtproto://host:443", "socks4://"} {
		_, err := tg.ParseProxyURL(raw)
		require.Error(t, err, "url %q", raw)
		assert.NotErrorIs(t, err, tg.ErrWebProxyNotWired, "url %q", raw)
	}
}

func TestParseProxyURLWebProxyNotWired(t *testing.T) {
	t.Parallel()

	_, err := tg.ParseProxyURL("webproxy://relay.example/secret?carrier=websocket")
	require.ErrorIs(t, err, tg.ErrWebProxyNotWired)
	assert.NotContains(t, err.Error(), "panic")
}
