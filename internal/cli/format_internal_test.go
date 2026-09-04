package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOutputFormatAcceptsKnownValues(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]OutputFormat{
		"table": FormatTable,
		"json":  FormatJSON,
		"plain": FormatPlain,
	} {
		got, err := ParseOutputFormat(value)
		require.NoError(t, err, value)
		assert.Equal(t, want, got)
	}
}

func TestParseOutputFormatRejectsUnknownValue(t *testing.T) {
	t.Parallel()

	_, err := ParseOutputFormat("xml")
	require.ErrorIs(t, err, errBadFormat)
	assert.Contains(t, err.Error(), "table")
	assert.Contains(t, err.Error(), "json")
	assert.Contains(t, err.Error(), "plain")
}

func TestPrintJSONGolden(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	err := printJSON(cmd, struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}{"probe", 2})
	require.NoError(t, err)

	assert.JSONEq(t, `{"name": "probe", "n": 2}`, out.String())
	assert.True(t, strings.HasSuffix(out.String(), "\n"), "json output ends with a newline")
}

func TestPrintPlainRowsGolden(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	err := printPlainRows(cmd,
		[]string{"id", "title"},
		[][]string{{"1", "alpha"}, {"2", "beta"}})
	require.NoError(t, err)

	assert.Equal(t, "id: 1\ntitle: alpha\n\nid: 2\ntitle: beta\n", out.String())
}

func TestPrintPlainPairsGolden(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	err := printPlainPairs(cmd, [][2]string{{"proxy", "(direct)"}, {"source", "direct"}})
	require.NoError(t, err)

	assert.Equal(t, "proxy: (direct)\nsource: direct\n", out.String())
}

func TestPrintPlainRowsEmpty(t *testing.T) {
	t.Parallel()

	cmd, out := newOutCmd()

	require.NoError(t, printPlainRows(cmd, []string{"id"}, nil))
	assert.Empty(t, out.String(), "no rows must render nothing")
}
