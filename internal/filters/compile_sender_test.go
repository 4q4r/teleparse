package filters_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileSenderPredicates(t *testing.T) {
	t.Parallel()

	contactCtx := baseContext()
	contactCtx.Sender.IsContact = true
	contactCtx.Sender.IsMutual = true

	oneWayCtx := baseContext()
	oneWayCtx.Sender.IsContact = true

	outCtx := baseContext()
	outCtx.Message.Out = true

	otherUserCtx := baseContext()
	otherUserCtx.Sender.ID = 999
	otherUserCtx.Sender.Username = "bob"

	phoneCtx := baseContext()
	phoneCtx.Sender.Phone = "+79001234567"

	spacedPhoneCtx := baseContext()
	spacedPhoneCtx.Sender.Phone = "+7 999 123-45-67"

	runPredicateCases(t, []predCase{
		{
			name: "contacts only matches contact",
			set:  func(o *filters.Options) { o.ContactsOnly = true },
			ctx:  contactCtx, want: true,
		},
		{
			name: "contacts only rejects stranger",
			set:  func(o *filters.Options) { o.ContactsOnly = true },
			ctx:  baseContext(), want: false,
		},
		{
			name: "mutual only matches mutual contact",
			set:  func(o *filters.Options) { o.MutualOnly = true },
			ctx:  contactCtx, want: true,
		},
		{
			name: "mutual only rejects one-way contact",
			set:  func(o *filters.Options) { o.MutualOnly = true },
			ctx:  oneWayCtx, want: false,
		},
		{
			name: "from me true matches outgoing message",
			set:  func(o *filters.Options) { o.FromMe = filters.Tri(true) },
			ctx:  outCtx, want: true,
		},
		{
			name: "from me true rejects incoming message",
			set:  func(o *filters.Options) { o.FromMe = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "from me false matches incoming message",
			set:  func(o *filters.Options) { o.FromMe = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "from me false rejects outgoing message",
			set:  func(o *filters.Options) { o.FromMe = filters.Tri(false) },
			ctx:  outCtx, want: false,
		},
		{
			name: "from users matches numeric id",
			set:  func(o *filters.Options) { o.FromUsers = []string{"20"} },
			ctx:  baseContext(), want: true,
		},
		{
			name: "from users matches handle case insensitive",
			set:  func(o *filters.Options) { o.FromUsers = []string{"@ALICE"} },
			ctx:  baseContext(), want: true,
		},
		{
			name: "from users rejects other sender",
			set:  func(o *filters.Options) { o.FromUsers = []string{"20", "carol"} },
			ctx:  otherUserCtx, want: false,
		},
		{
			name: "exclude users skips listed sender",
			set:  func(o *filters.Options) { o.ExcludeUsers = []string{"20"} },
			ctx:  baseContext(), want: false,
		},
		{
			name: "exclude users keeps unlisted sender",
			set:  func(o *filters.Options) { o.ExcludeUsers = []string{"20"} },
			ctx:  otherUserCtx, want: true,
		},
		{
			name: "from users matches exact phone digits",
			set:  func(o *filters.Options) { o.FromUsers = []string{"+79001234567"} },
			ctx:  phoneCtx, want: true,
		},
		{
			name: "from users matches phone entry with tolerated separators",
			set:  func(o *filters.Options) { o.FromUsers = []string{"+7 900 123-45-67"} },
			ctx:  phoneCtx, want: true,
		},
		{
			name: "from users matches bare digits against stored formatted phone",
			set:  func(o *filters.Options) { o.FromUsers = []string{"79991234567"} },
			ctx:  spacedPhoneCtx, want: true,
		},
		{
			name: "from users phone entry without country code never matches",
			set:  func(o *filters.Options) { o.FromUsers = []string{"89991234567"} },
			ctx:  phoneCtx, want: false,
		},
		{
			name: "from users national phone entry never matches international digits",
			set:  func(o *filters.Options) { o.FromUsers = []string{"89991234567"} },
			ctx:  spacedPhoneCtx, want: false,
		},
		{
			name: "from users phone entry with different digit count never matches",
			set:  func(o *filters.Options) { o.FromUsers = []string{"7900123456"} },
			ctx:  phoneCtx, want: false,
		},
		{
			name: "from users mixed list matches any of id username phone",
			set:  func(o *filters.Options) { o.FromUsers = []string{"999", "@carol", "+79001234567"} },
			ctx:  phoneCtx, want: true,
		},
		{
			name: "from users phone entry never matches empty phone sender",
			set:  func(o *filters.Options) { o.FromUsers = []string{"+79001234567"} },
			ctx:  baseContext(), want: false,
		},
		{
			name: "from users id still matches when phone set",
			set:  func(o *filters.Options) { o.FromUsers = []string{"20"} },
			ctx:  phoneCtx, want: true,
		},
		{
			name: "exclude users skips listed phone",
			set:  func(o *filters.Options) { o.ExcludeUsers = []string{"+79001234567"} },
			ctx:  phoneCtx, want: false,
		},
		{
			name: "exclude users keeps sender with other phone",
			set:  func(o *filters.Options) { o.ExcludeUsers = []string{"+79110000000"} },
			ctx:  phoneCtx, want: true,
		},
		{
			name: "sender name regex matches prefix",
			set:  func(o *filters.Options) { o.SenderNameRegex = `^Ali` },
			ctx:  baseContext(), want: true,
		},
		{
			name: "sender name regex rejects name",
			set:  func(o *filters.Options) { o.SenderNameRegex = `^Bob` },
			ctx:  baseContext(), want: false,
		},
		{
			name: "sender username regex matches handle",
			set:  func(o *filters.Options) { o.SenderUsernameRegex = `^al` },
			ctx:  baseContext(), want: true,
		},
		{
			name: "sender username regex rejects handle",
			set:  func(o *filters.Options) { o.SenderUsernameRegex = `^bob` },
			ctx:  baseContext(), want: false,
		},
		{
			name: "sender phone regex matches number",
			set:  func(o *filters.Options) { o.SenderPhoneRegex = `^\+7` },
			ctx:  phoneCtx, want: true,
		},
		{
			name: "sender phone regex rejects number",
			set:  func(o *filters.Options) { o.SenderPhoneRegex = `^\+1` },
			ctx:  phoneCtx, want: false,
		},
	})
}

func TestCompileSenderTriFlags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		set    func(*filters.Options)
		sender func(*filters.Sender)
		want   bool
	}{
		{
			name:   "bot true matches bot sender",
			set:    func(o *filters.Options) { o.SenderBot = filters.Tri(true) },
			sender: func(s *filters.Sender) { s.IsBot = true }, want: true,
		},
		{
			name: "bot true rejects human sender",
			set:  func(o *filters.Options) { o.SenderBot = filters.Tri(true) },
			want: false,
		},
		{
			name: "bot false matches human sender",
			set:  func(o *filters.Options) { o.SenderBot = filters.Tri(false) },
			want: true,
		},
		{
			name:   "bot false rejects bot sender",
			set:    func(o *filters.Options) { o.SenderBot = filters.Tri(false) },
			sender: func(s *filters.Sender) { s.IsBot = true }, want: false,
		},
		{
			name:   "premium true matches premium sender",
			set:    func(o *filters.Options) { o.SenderPremium = filters.Tri(true) },
			sender: func(s *filters.Sender) { s.IsPremium = true }, want: true,
		},
		{
			name:   "premium false rejects premium sender",
			set:    func(o *filters.Options) { o.SenderPremium = filters.Tri(false) },
			sender: func(s *filters.Sender) { s.IsPremium = true }, want: false,
		},
		{
			name:   "verified true matches verified sender",
			set:    func(o *filters.Options) { o.SenderVerified = filters.Tri(true) },
			sender: func(s *filters.Sender) { s.IsVerified = true }, want: true,
		},
		{
			name:   "verified false rejects verified sender",
			set:    func(o *filters.Options) { o.SenderVerified = filters.Tri(false) },
			sender: func(s *filters.Sender) { s.IsVerified = true }, want: false,
		},
		{
			name:   "scam true matches flagged sender",
			set:    func(o *filters.Options) { o.SenderScam = filters.Tri(true) },
			sender: func(s *filters.Sender) { s.IsScam = true }, want: true,
		},
		{
			name: "scam true rejects clean sender",
			set:  func(o *filters.Options) { o.SenderScam = filters.Tri(true) },
			want: false,
		},
		{
			name: "deleted false matches live account",
			set:  func(o *filters.Options) { o.SenderDeleted = filters.Tri(false) },
			want: true,
		},
		{
			name:   "deleted false rejects deleted account",
			set:    func(o *filters.Options) { o.SenderDeleted = filters.Tri(false) },
			sender: func(s *filters.Sender) { s.IsDeleted = true }, want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := &filters.Options{}
			tc.set(opts)

			plan, err := filters.Compile(opts)
			require.NoError(t, err)

			ctx := baseContext()
			if tc.sender != nil {
				tc.sender(&ctx.Sender)
			}

			assert.Equal(t, tc.want, planMatches(plan, ctx))
		})
	}
}

func TestCompileSenderTriUnsetAddsNoPredicates(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	names := predicateNames(plan)
	assert.NotContains(t, names, "from_me")
	assert.NotContains(t, names, "sender_bot")
	assert.NotContains(t, names, "sender_premium")
	assert.NotContains(t, names, "sender_verified")
	assert.NotContains(t, names, "sender_scam")
	assert.NotContains(t, names, "sender_deleted")
}
