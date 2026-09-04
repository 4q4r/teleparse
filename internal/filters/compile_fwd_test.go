package filters_test

import (
	"teleparse/internal/filters"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forwardedContext stamps origins at 2023-11-14T22:13:20Z.
const baseForwardUnix = 1_700_000_000

func forwardedContext(mutate func(*filters.ForwardInfo)) *filters.Context {
	ctx := baseContext()
	fwd := &filters.ForwardInfo{Present: true, FromID: 42, FromUsername: "newsbot", Date: baseForwardUnix}

	if mutate != nil {
		mutate(fwd)
	}

	ctx.Message.Forward = fwd

	return ctx
}

func TestCompileForwardPredicates(t *testing.T) {
	t.Parallel()

	runPredicateCases(t, []predCase{
		{
			name: "forwarded true matches forwarded message",
			set:  func(o *filters.Options) { o.Forwarded = filters.Tri(true) },
			ctx:  forwardedContext(nil), want: true,
		},
		{
			name: "forwarded true rejects plain message",
			set:  func(o *filters.Options) { o.Forwarded = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "forwarded false matches plain message",
			set:  func(o *filters.Options) { o.Forwarded = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "forwarded false rejects forwarded message",
			set:  func(o *filters.Options) { o.Forwarded = filters.Tri(false) },
			ctx:  forwardedContext(nil), want: false,
		},
		{
			name: "fwd from matches numeric origin id",
			set:  func(o *filters.Options) { o.FwdFrom = []string{"42"} },
			ctx:  forwardedContext(nil), want: true,
		},
		{
			name: "fwd from matches origin username case insensitive",
			set:  func(o *filters.Options) { o.FwdFrom = []string{"@NewsBot"} },
			ctx:  forwardedContext(nil), want: true,
		},
		{
			name: "fwd from rejects other origin",
			set:  func(o *filters.Options) { o.FwdFrom = []string{"42", "other"} },
			ctx:  forwardedContext(func(fwd *filters.ForwardInfo) { fwd.FromID = 99; fwd.FromUsername = "elsewhere" }),
			want: false,
		},
		{
			name: "fwd from never matches without forward",
			set:  func(o *filters.Options) { o.FwdFrom = []string{"42"} },
			ctx:  baseContext(), want: false,
		},
		{
			name: "fwd hidden true matches hidden origin",
			set:  func(o *filters.Options) { o.FwdHidden = filters.Tri(true) },
			ctx:  forwardedContext(func(fwd *filters.ForwardInfo) { fwd.Hidden = true }), want: true,
		},
		{
			name: "fwd hidden true rejects visible origin",
			set:  func(o *filters.Options) { o.FwdHidden = filters.Tri(true) },
			ctx:  forwardedContext(nil), want: false,
		},
		{
			name: "fwd hidden false matches visible origin",
			set:  func(o *filters.Options) { o.FwdHidden = filters.Tri(false) },
			ctx:  forwardedContext(nil), want: true,
		},
		{
			name: "fwd hidden false matches plain message",
			set:  func(o *filters.Options) { o.FwdHidden = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
		{
			name: "fwd date after inclusive at exact boundary",
			set:  func(o *filters.Options) { o.FwdDateAfter = "2023-11-14T22:13:20Z" },
			ctx:  forwardedContext(nil), want: true,
		},
		{
			name: "fwd date after rejects older origin",
			set:  func(o *filters.Options) { o.FwdDateAfter = "2023-11-15" },
			ctx:  forwardedContext(nil), want: false,
		},
		{
			name: "fwd date before inclusive at exact boundary",
			set:  func(o *filters.Options) { o.FwdDateBefore = "2023-11-14T22:13:20Z" },
			ctx:  forwardedContext(nil), want: true,
		},
		{
			name: "fwd date before rejects newer origin",
			set:  func(o *filters.Options) { o.FwdDateBefore = "2023-11-14T00:00:00Z" },
			ctx:  forwardedContext(nil), want: false,
		},
		{
			name: "fwd date after never matches without forward",
			set:  func(o *filters.Options) { o.FwdDateAfter = "2000-01-01" },
			ctx:  baseContext(), want: false,
		},
		{
			name: "fwd date before never matches without forward",
			set:  func(o *filters.Options) { o.FwdDateBefore = "2030-01-01" },
			ctx:  baseContext(), want: false,
		},
	})
}

func TestCompileForwardExplainLines(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{
		Forwarded:     filters.Tri(true),
		FwdFrom:       []string{"42"},
		FwdHidden:     filters.Tri(false),
		FwdDateAfter:  "2023-11-01",
		FwdDateBefore: "2023-12-01",
	}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	assert.Contains(t, plan.ExplainLines, "client: forwarded=true")
	assert.Contains(t, plan.ExplainLines, "client: fwd_from=42")
	assert.Contains(t, plan.ExplainLines, "client: fwd_hidden=false")
	assert.Contains(t, plan.ExplainLines, "client: fwd_date_after=2023-11-01")
	assert.Contains(t, plan.ExplainLines, "client: fwd_date_before=2023-12-01")
}
