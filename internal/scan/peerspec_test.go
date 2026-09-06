package scan_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/scan"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePeerSpec(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec string
		want scan.PeerRef
	}{
		{name: "at handle", spec: "@durov", want: scan.PeerRef{Username: "durov"}},
		{name: "bare handle lowercased", spec: "Durov", want: scan.PeerRef{Username: "durov"}},
		{name: "t.me link", spec: "https://t.me/durov", want: scan.PeerRef{Username: "durov"}},
		{name: "bare id", spec: "123456", want: scan.PeerRef{ID: 123456}},
		{name: "-100 prefixed id", spec: "-100123456", want: scan.PeerRef{ID: 123456}},
		{name: "channel link", spec: "t.me/c/123456", want: scan.PeerRef{ID: 123456}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := scan.ParsePeerSpec(tc.spec)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParsePeerSpecRejectsNonChatSpecs(t *testing.T) {
	t.Parallel()

	for _, spec := range []string{"", "a/b", "https://t.me/c/123456/789", " "} {
		_, err := scan.ParsePeerSpec(spec)
		require.Error(t, err, "spec %q must not parse as a chat spec", spec)
		assert.ErrorIs(t, err, scan.ErrUnknownChat)
	}
}

func TestPeerRefMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		ref      scan.PeerRef
		username string
		chatID   int64
		want     bool
	}{
		{name: "id match", ref: scan.PeerRef{ID: 42}, chatID: 42, want: true},
		{name: "id mismatch", ref: scan.PeerRef{ID: 42}, chatID: 43, want: false},
		{name: "username exact", ref: scan.PeerRef{Username: "news"}, username: "news", want: true},
		{name: "username case and at-sign insensitive", ref: scan.PeerRef{Username: "news"}, username: "@News", want: true},
		{name: "username mismatch", ref: scan.PeerRef{Username: "news"}, username: "other", want: false},
		{name: "empty ref never matches", ref: scan.PeerRef{}, username: "news", chatID: 42, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.ref.Matches(tc.username, tc.chatID))
		})
	}
}
