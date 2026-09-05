package cli_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/cli"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFullWalkFlagOnDlFamily(t *testing.T) {
	t.Parallel()

	root := cli.New()

	for _, name := range []string{"dl", "scan", "sync"} {
		cmd := commandByName(t, root, name)

		flag := cmd.Flags().Lookup("full")
		require.NotNil(t, flag, "%s must carry the --full opt-out", name)
		assert.Equal(t, "bool", flag.Value.Type())
		assert.Equal(t, "false", flag.DefValue, "--full defaults off; incremental is the default behavior")
	}
}
