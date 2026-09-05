package filters_test

import (
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/filters"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseContext stamps messages at 2025-06-15T15:06:40Z.
const baseMessageUnix = 1_750_000_000

func TestCompileDatePredicates(t *testing.T) {
	t.Parallel()

	boundaryCtx := flaggedContext(func(msg *filters.Message) { msg.Date = baseMessageUnix })

	runPredicateCases(t, []predCase{
		{
			name: "after inclusive at exact boundary",
			set:  func(o *filters.Options) { o.After = "2025-06-15T15:06:40Z" },
			ctx:  boundaryCtx, want: true,
		},
		{
			name: "after rejects older message",
			set:  func(o *filters.Options) { o.After = "2025-06-16" },
			ctx:  baseContext(), want: false,
		},
		{
			name: "after matches newer message",
			set:  func(o *filters.Options) { o.After = "2025-06-01" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "before inclusive at exact boundary",
			set:  func(o *filters.Options) { o.Before = "2025-06-15T15:06:40Z" },
			ctx:  boundaryCtx, want: true,
		},
		{
			name: "before rejects newer message",
			set:  func(o *filters.Options) { o.Before = "2025-06-15" },
			ctx:  baseContext(), want: false,
		},
		{
			name: "before matches older message",
			set:  func(o *filters.Options) { o.Before = "2025-07-01" },
			ctx:  baseContext(), want: true,
		},
		{
			name: "last matches recent message",
			set:  func(o *filters.Options) { o.Last = "1h" },
			ctx:  recentContext(30 * time.Minute), want: true,
		},
		{
			name: "last rejects stale message",
			set:  func(o *filters.Options) { o.Last = "1h" },
			ctx:  recentContext(2 * time.Hour), want: false,
		},
		{
			name: "older than matches stale message",
			set:  func(o *filters.Options) { o.OlderThan = "1h" },
			ctx:  recentContext(2 * time.Hour), want: true,
		},
		{
			name: "older than rejects recent message",
			set:  func(o *filters.Options) { o.OlderThan = "1h" },
			ctx:  recentContext(30 * time.Minute), want: false,
		},
		{
			name: "edited true matches edited message",
			set:  func(o *filters.Options) { o.Edited = filters.Tri(true) },
			ctx:  flaggedContext(func(msg *filters.Message) { msg.HasEdit = true }), want: true,
		},
		{
			name: "edited true rejects unedited message",
			set:  func(o *filters.Options) { o.Edited = filters.Tri(true) },
			ctx:  baseContext(), want: false,
		},
		{
			name: "edited false matches unedited message",
			set:  func(o *filters.Options) { o.Edited = filters.Tri(false) },
			ctx:  baseContext(), want: true,
		},
	})
}

func recentContext(ago time.Duration) *filters.Context {
	return flaggedContext(func(msg *filters.Message) { msg.Date = time.Now().Add(-ago).Unix() })
}

func TestCompileDatesUnsetAddNoPredicates(t *testing.T) {
	t.Parallel()

	plan, err := filters.Compile(&filters.Options{})
	require.NoError(t, err)

	names := predicateNames(plan)
	assert.NotContains(t, names, "after")
	assert.NotContains(t, names, "before")
	assert.NotContains(t, names, "edited")
}

func TestCompileDateExplainLines(t *testing.T) {
	t.Parallel()

	opts := &filters.Options{After: "2025-06-01", OlderThan: "2d", Edited: filters.Tri(true)}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)

	assert.Contains(t, plan.ExplainLines, "client: after=2025-06-01")
	assert.Contains(t, plan.ExplainLines, "client: older_than=2d")
	assert.Contains(t, plan.ExplainLines, "client: edited=true")
}
