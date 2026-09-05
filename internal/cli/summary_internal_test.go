package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// summaryCommand wires printSummary onto a capturing stdout.
func summaryCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer

	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(&out)

	return cmd, &out
}

func TestPrintSummaryShowsLinkedWhenPresent(t *testing.T) {
	t.Parallel()

	cmd, out := summaryCommand()

	require.NoError(t, printSummary(cmd, download.Result{Downloaded: 2, Linked: 3}, time.Second))

	assert.Contains(t, out.String(), "downloaded: 2")
	assert.Contains(t, out.String(), "linked: 3")
}

func TestPrintSummaryOmitsLinkedWhenZero(t *testing.T) {
	t.Parallel()

	cmd, out := summaryCommand()

	require.NoError(t, printSummary(cmd, download.Result{Downloaded: 1}, time.Second))

	assert.NotContains(t, out.String(), "linked")
}
