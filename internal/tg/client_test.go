package tg_test

import (
	"context"
	"testing"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunRejectsUnsetCreds covers the entry guard: a zero credential pair
// fails before any account storage, lock or dial is touched; the cli layer
// owns resolution and surfaces the actionable help error.
func TestRunRejectsUnsetCreds(t *testing.T) {
	t.Parallel()

	run := func(context.Context, *telegram.Client) error { return nil }

	err := tg.Run(t.Context(), "main", tg.Creds{}, &config.Config{}, &config.Paths{}, run)
	require.ErrorIs(t, err, tg.ErrCredsUnset)

	err = tg.Run(t.Context(), "main", tg.Creds{APIID: 1}, &config.Config{}, &config.Paths{}, run)
	require.ErrorIs(t, err, tg.ErrCredsUnset)

	err = tg.Run(t.Context(), "main", tg.Creds{APIHash: "h"}, &config.Config{}, &config.Paths{}, run)
	require.ErrorIs(t, err, tg.ErrCredsUnset)
}

func TestBuildMiddlewares(t *testing.T) {
	t.Parallel()

	off := tg.BuildMiddlewares(config.Pacing{RequestsPerMinute: 0})
	assert.Len(t, off, 1, "floodwait middleware must always be present")

	on := tg.BuildMiddlewares(config.Pacing{RequestsPerMinute: 120})
	assert.Len(t, on, 2, "ratelimit middleware must be added when rpm > 0")
}
