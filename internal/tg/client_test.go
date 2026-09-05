package tg_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredsFromEnv(t *testing.T) {
	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")

	_, _, err := tg.CredsFromEnv()
	require.ErrorIs(t, err, tg.ErrAPICredsMissing)

	t.Setenv("TELEPARSE_API_ID", "123456")
	t.Setenv("TELEPARSE_API_HASH", "deadbeefcafe")

	apiID, apiHash, err := tg.CredsFromEnv()
	require.NoError(t, err)
	assert.Equal(t, int64(123456), apiID)
	assert.Equal(t, "deadbeefcafe", apiHash)

	t.Setenv("TELEPARSE_API_ID", "not-a-number")
	t.Setenv("TELEPARSE_API_HASH", "hash")

	_, _, err = tg.CredsFromEnv()
	require.Error(t, err)
}

func TestBuildMiddlewares(t *testing.T) {
	t.Parallel()

	off := tg.BuildMiddlewares(config.Pacing{RequestsPerMinute: 0})
	assert.Len(t, off, 1, "floodwait middleware must always be present")

	on := tg.BuildMiddlewares(config.Pacing{RequestsPerMinute: 120})
	assert.Len(t, on, 2, "ratelimit middleware must be added when rpm > 0")
}
