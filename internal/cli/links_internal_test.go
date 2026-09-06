package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLinksGrammar(t *testing.T) {
	t.Parallel()

	single := func(ids ...int) []int { return ids }

	for name, tc := range map[string]struct {
		links []string
		want  []linkTarget
	}{
		"plain https link": {
			links: []string{"https://t.me/news/100"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100)}},
		},
		"bare t.me link": {
			links: []string{"t.me/news/100"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100)}},
		},
		"telegram.me alias folds onto t.me": {
			links: []string{"https://telegram.me/News/100"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100)}},
		},
		"range expands inclusive": {
			links: []string{"t.me/news/100-102"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100, 101, 102)}},
		},
		"comma list": {
			links: []string{"t.me/news/100,102,105"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100, 102, 105)}},
		},
		"mixed list and range sorted": {
			links: []string{"t.me/news/105,100-102"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100, 101, 102, 105)}},
		},
		"private channel link": {
			links: []string{"https://t.me/c/123456/789"},
			want:  []linkTarget{{PeerSpec: "t.me/c/123456", MsgIDs: single(789)}},
		},
		"private channel topic path": {
			links: []string{"t.me/c/123456/7/89"},
			want:  []linkTarget{{PeerSpec: "t.me/c/123456", TopicID: 7, MsgIDs: single(89)}},
		},
		"public forum topic path": {
			links: []string{"t.me/forumchat/7/89"},
			want:  []linkTarget{{PeerSpec: "t.me/forumchat", TopicID: 7, MsgIDs: single(89)}},
		},
		"thread comment context under post": {
			links: []string{"t.me/news/500?thread=550"},
			want:  []linkTarget{{PeerSpec: "t.me/news", TopicID: 500, MsgIDs: single(550)}},
		},
		"comment param alias": {
			links: []string{"t.me/news/500?comment=551"},
			want:  []linkTarget{{PeerSpec: "t.me/news", TopicID: 500, MsgIDs: single(551)}},
		},
		"thread and comment together dedupe": {
			links: []string{"t.me/news/500?thread=550&comment=550"},
			want:  []linkTarget{{PeerSpec: "t.me/news", TopicID: 500, MsgIDs: single(550)}},
		},
		"tg resolve scheme": {
			links: []string{"tg://resolve?domain=news&post=100"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100)}},
		},
		"same peer links merge across args": {
			links: []string{"t.me/news/105", "https://t.me/news/100", "t.me/news/105"},
			want:  []linkTarget{{PeerSpec: "t.me/news", MsgIDs: single(100, 105)}},
		},
		"same peer distinct topics stay apart": {
			links: []string{"t.me/news/7/10", "t.me/news/9/20"},
			want: []linkTarget{
				{PeerSpec: "t.me/news", TopicID: 7, MsgIDs: single(10)},
				{PeerSpec: "t.me/news", TopicID: 9, MsgIDs: single(20)},
			},
		},
		"distinct peers stay apart": {
			links: []string{"t.me/news/100", "t.me/c/55/200"},
			want: []linkTarget{
				{PeerSpec: "t.me/news", MsgIDs: single(100)},
				{PeerSpec: "t.me/c/55", MsgIDs: single(200)},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := parseLinks(tc.links)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseLinksErrors(t *testing.T) {
	t.Parallel()

	for name, link := range map[string]string{
		"username without message id":   "t.me/news",
		"https username without id":     "https://t.me/news",
		"private channel without id":    "t.me/c/123456",
		"non-numeric message id":        "t.me/news/abc",
		"zero message id":               "t.me/news/0",
		"reversed range":                "t.me/news/200-100",
		"empty id list":                 "t.me/news/100,,102",
		"unsupported host":              "https://example.com/news/100",
		"too many topic segments":       "t.me/c/123456/7/89/12",
		"non-numeric topic":             "t.me/c/123456/abc/89",
		"tg resolve without post":       "tg://resolve?domain=news",
		"tg resolve without domain":     "tg://resolve?post=100",
		"message id in topic slot only": "t.me/news/7/",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parseLinks([]string{link})
			assert.Error(t, err, "%q must not parse", link)
		})
	}
}

func TestParseLinksRangeSpanGuard(t *testing.T) {
	t.Parallel()

	_, err := parseLinks([]string{"t.me/news/1-100001"})
	require.Error(t, err, "a range wider than the span guard must be rejected")
}
