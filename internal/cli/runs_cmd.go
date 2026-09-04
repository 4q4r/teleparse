package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"teleparse/internal/store"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// runsListLimit caps the default `runs list` output.
const runsListLimit = 50

// cleanOlderThanDefault drives `runs clean` when no duration is given.
const cleanOlderThanDefault = 7 * 24 * time.Hour

// dataDirPerm is the state-directory permission.
const dataDirPerm = 0o700

func runsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Inspect download runs",
	}

	var olderThan string

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List runs",
			Args:  cobra.NoArgs,
			RunE:  func(c *cobra.Command, _ []string) error { return runsList(app, c) },
		},
		&cobra.Command{
			Use:   "show RUN_ID",
			Short: "Show run details",
			Args:  cobra.ExactArgs(1),
			RunE:  func(c *cobra.Command, args []string) error { return runsShow(app, c, args[0]) },
		},
		&cobra.Command{
			Use:   "clean",
			Short: "Delete finished runs from index",
			Args:  cobra.NoArgs,
			RunE:  func(c *cobra.Command, _ []string) error { return runsClean(app, c, olderThan) },
		},
	)
	cmd.PersistentFlags().StringVar(&olderThan, "older-than", "168h",
		"clean finished runs older than this duration (e.g. 168h)")

	return cmd
}

func runsList(app *App, cmd *cobra.Command) error {
	state, err := openStore(app)
	if err != nil {
		return fail(cmd, err)
	}

	defer func() { _ = state.Close() }()

	runs, err := state.ListRuns(cmd.Context(), runsListLimit)
	if err != nil {
		return fail(cmd, fmt.Errorf("list runs: %w", err))
	}

	if len(runs) == 0 {
		return printLine(cmd, "no runs\n")
	}

	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(writer, "RUN\tACCOUNT\tSTATUS\tSTARTED\tRESUME_AT"); err != nil {
		return fail(cmd, fmt.Errorf("write runs header: %w", err))
	}

	for _, run := range runs {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			run.RunID, run.Account, run.Status, run.StartedAt, textOrDefault(run.ResumeAt)); err != nil {
			return fail(cmd, fmt.Errorf("write run row: %w", err))
		}
	}

	if err := writer.Flush(); err != nil {
		return fail(cmd, fmt.Errorf("flush runs table: %w", err))
	}

	return nil
}

func runsShow(app *App, cmd *cobra.Command, runID string) error {
	state, err := openStore(app)
	if err != nil {
		return fail(cmd, err)
	}

	defer func() { _ = state.Close() }()

	run, exists, err := state.GetRun(cmd.Context(), runID)
	if err != nil {
		return fail(cmd, fmt.Errorf("load run %s: %w", runID, err))
	}

	if !exists {
		return fail(cmd, fmt.Errorf("run %s: %w", runID, errRunNotFound))
	}

	detail := fmt.Sprintf("run id:    %s\naccount:   %s\nprofile:   %s\nstatus:    %s\n"+
		"started:   %s\nfinished:  %s\nresume_at: %s\nerror:     %s\n",
		run.RunID, run.Account, textOrDefault(run.Profile), run.Status,
		run.StartedAt, textOrDefault(run.FinishedAt), textOrDefault(run.ResumeAt), textOrDefault(run.Error))

	if err := printLine(cmd, "%s\nfilters (toml):\n%s\n", detail, run.FilterJSON); err != nil {
		return fail(cmd, err)
	}

	return nil
}

func runsClean(app *App, cmd *cobra.Command, olderThan string) error {
	parsed, err := time.ParseDuration(olderThan)
	if err != nil {
		return fail(cmd, fmt.Errorf("parse --older-than %q: %w", olderThan, err))
	}

	if parsed <= 0 {
		parsed = cleanOlderThanDefault
	}

	state, err := openStore(app)
	if err != nil {
		return fail(cmd, err)
	}

	defer func() { _ = state.Close() }()

	removed, err := state.CleanupFinishedRuns(cmd.Context(), time.Now().Add(-parsed))
	if err != nil {
		return fail(cmd, fmt.Errorf("clean runs: %w", err))
	}

	return printLine(cmd, "removed %d finished run(s)\n", removed)
}

// openStore opens the state database for the command surfaces, creating
// the data directory on first use.
func openStore(app *App) (*store.Store, error) {
	if err := os.MkdirAll(filepath.Dir(app.paths.StateDB), dataDirPerm); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.Open(app.paths.StateDB)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}

	return st, nil
}
