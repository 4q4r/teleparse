package filters_test

import (
	"errors"
	"teleparse/internal/filters"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSizeUnits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "500", want: 500},
		{in: "10KB", want: 10_000},
		{in: "20MB", want: 20_000_000},
		{in: "1.5GiB", want: 1_610_612_736},
		{in: "2g", want: 2_000_000_000},
		{in: "1kib", want: 1024},
		{in: "MB", want: 1_000_000},
		{in: "", wantErr: true},
		{in: "  ", wantErr: true},
		{in: "-5MB", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "12XZ", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, err := filters.ParseSize(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, filters.ErrEmptySize) || errors.Is(err, filters.ErrNegativeSize) ||
					errors.Is(err, filters.ErrBadSizeNumber) || errors.Is(err, filters.ErrUnknownSizeUnit) ||
					errors.Is(err, filters.ErrBadParse), "unexpected sentinel: %v", err)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseRelative(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "30s", want: 30 * time.Second},
		{in: "12h", want: 12 * time.Hour},
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "2w", want: 14 * 24 * time.Hour},
		{in: "1h30m", want: 90 * time.Minute},
		{in: "90", wantErr: true},
		{in: "", wantErr: true},
		{in: "d", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, err := filters.ParseRelative(tc.in)
			if tc.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseTimeAbsoluteAndRelative(t *testing.T) {
	t.Parallel()

	abs, err := filters.ParseTime("2026-01-02")
	require.NoError(t, err)
	assert.Equal(t, 2026, abs.Year())
	assert.Equal(t, time.January, abs.Month())

	rfc, err := filters.ParseTime("2026-01-02T15:04:05Z")
	require.NoError(t, err)
	assert.Equal(t, 15, rfc.UTC().Hour())

	rel, err := filters.ParseTime("1h")
	require.NoError(t, err)
	assert.InDelta(t, time.Now().Add(-time.Hour).Unix(), rel.Unix(), 5)

	_, err = filters.ParseTime("not-a-time")
	require.Error(t, err)
	assert.ErrorIs(t, err, filters.ErrBadTimeFormat)
}
