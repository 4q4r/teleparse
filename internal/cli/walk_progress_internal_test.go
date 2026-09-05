package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// walkModeMatrix enumerates every runMode shape that walks chats; the
// walk progress surface must cover all of them, not just previews.
func walkModeMatrix() map[string]runMode {
	return map[string]runMode{
		"plain dl":   {},
		"sync":       {syncMode: true},
		"dry-run":    {dryRun: true},
		"count-only": {countOnly: true},
	}
}

func TestWalkProgressForCoversAllWalkModes(t *testing.T) {
	t.Parallel()

	for name, mode := range walkModeMatrix() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd, _ := newErrCmd()

			progress := walkProgressFor(mode, cmd, &App{}, 3)

			require.NotNil(t, progress, "the walk phase must not fall silent in %s runs", name)
			assert.False(t, progress.tty, "tests run without a stderr terminal, so the line surface applies")
			assert.Equal(t, 3, progress.total)

			progress.close()
		})
	}
}

func TestWalkProgressForSilentAndEmptyScopeStayNil(t *testing.T) {
	t.Parallel()

	silent := &cobra.Command{}
	silent.Flags().Bool("silent-output", true, "")

	assert.Nil(t, walkProgressFor(runMode{}, silent, &App{}, 5), "-s suppresses the walk surface")

	cmd, _ := newErrCmd()

	assert.Nil(t, walkProgressFor(runMode{dryRun: true}, cmd, &App{}, 0), "nothing to walk means no surface")
}

func TestWalkTransitionRendersCachedSegmentOnlyWhenPresent(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"walked 254 chats, matched 1024 files - downloading",
		walkTransition(254, 1024, 0))

	assert.Equal(t,
		"walked 254 chats, matched 1024 files (+87 cached) - downloading",
		walkTransition(254, 1024, 87))
}

func TestPrintWalkTransitionWritesDimLine(t *testing.T) {
	t.Parallel()

	cmd, stderr := newErrCmd()

	require.NoError(t, printWalkTransition(cmd, &App{}, 12, 345, 9))
	assert.Contains(t, stderr.String(), "walked 12 chats, matched 345 files (+9 cached) - downloading\n")
}

func TestPrintWalkTransitionSilentWritesNothing(t *testing.T) {
	t.Parallel()

	cmd, stderr := newErrCmd()
	cmd.Flags().Bool("silent-output", true, "")

	require.NoError(t, printWalkTransition(cmd, &App{}, 12, 345, 0))
	assert.Empty(t, stderr.String())
}

func TestCachedTotalSumsIncrementalStash(t *testing.T) {
	t.Parallel()

	collector := &walkCollector{cached: map[int64]int64{1: 12, 2: 0, 3: 7}}

	assert.Equal(t, int64(19), cachedTotal(collector))
}

// TestWalkHandoverSettlesLinesBeforeTransitionPrint pins the executeRun
// ordering on the line surface: the walk progress closes fully before
// the transition line prints and the download reporter opens.
func TestWalkHandoverSettlesLinesBeforeTransitionPrint(t *testing.T) {
	t.Parallel()

	cmd, stderr := newErrCmd()

	progress := &scanProgress{out: cmd.ErrOrStderr(), total: 2, styler: NewStyler(false)}

	progress.chatDone("News", 4, 0, time.Second)
	progress.chatDone("Docs", 1, 0, time.Second)
	progress.close()

	require.NoError(t, printWalkTransition(cmd, &App{}, 2, 5, 0))

	out := stderr.String()

	assert.Less(t, strings.Index(out, "scanned 2/2"), strings.Index(out, "- downloading"),
		"the walk surface settles fully before the transition line")
}
