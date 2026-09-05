package cli_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/cli"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionShorthandPrintsVersion(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "-v")

	require.NoError(t, err)
	assert.Contains(t, out, "teleparse version")
}

func TestVersionLongFlagMatchesShorthand(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "--version")

	require.NoError(t, err)
	assert.Contains(t, out, "teleparse version")
}

func TestHelpShorthandRendersUsage(t *testing.T) {
	t.Parallel()

	out, err := execute(t, "-h")

	require.NoError(t, err)
	assert.Contains(t, out, "Usage")
	assert.Contains(t, out, "teleparse")
}

func TestSilentFlagRegisteredOnRoot(t *testing.T) {
	t.Parallel()

	root := cli.New()

	flag := root.PersistentFlags().Lookup("silent")
	require.NotNil(t, flag, "root must carry the persistent --silent flag")
	assert.Equal(t, "s", flag.Shorthand, "-s must be the global silent shorthand")
}

func TestDlExposesSilentOutputShorthand(t *testing.T) {
	t.Parallel()

	root := cli.New()

	for _, name := range []string{"dl", "scan", "sync"} {
		cmd := commandByName(t, root, name)

		flag := cmd.Flags().Lookup("silent-output")
		require.NotNil(t, flag, "%s must carry the -s mirror flag", name)
		assert.Equal(t, "s", flag.Shorthand, "%s must expose -s for output silence", name)

		// The message-property filter keeps the plain --silent long name.
		filter := cmd.Flags().Lookup("silent")
		require.NotNil(t, filter, "%s must keep the silently-sent filter", name)
		assert.Empty(t, filter.Shorthand, "the filter must never own -s")
	}
}

func TestDlHelpListsSilentShorthand(t *testing.T) {
	t.Parallel()

	root := cli.New()

	dl := commandByName(t, root, "dl")
	require.NotNil(t, dl)

	assert.Contains(t, dl.Flags().Lookup("silent-output").Usage, "suppress")
}

var _ = cobra.Command{}
