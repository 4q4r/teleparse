package tg_test

import (
	"teleparse/internal/tg"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPingDCRejectsBadProxyURL(t *testing.T) {
	t.Parallel()

	_, _, err := tg.PingDC(t.Context(), nil, "://missing-scheme", 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, tg.ErrBadProxyURL)
}

func TestProbeDCRejectsBadProxyURL(t *testing.T) {
	t.Parallel()

	_, err := tg.ProbeDC(t.Context(), "://missing-scheme", 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, tg.ErrBadProxyURL)
}
