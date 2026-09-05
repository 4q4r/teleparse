package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/4q4r/teleparse/internal/store"

	"github.com/spf13/cobra"
)

// runsListLimit caps the default `runs list` output.
const runsListLimit = 50

// cleanOlderThanFlagDefault and cleanOlderThanDefault drive `runs clean`:
// the flag text default and its duration form must stay in sync (168h == 7d).
const (
	cleanOlderThanFlagDefault = "168h"
	cleanOlderThanDefault     = 7 * 24 * time.Hour
)

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
			Use:     "list",
			Short:   "List runs",
			Example: "  teleparse runs list",
			Args:    cobra.NoArgs,
			RunE:    func(c *cobra.Command, _ []string) error { return runsList(app, c) },
		},
		&cobra.Command{
			Use:     "show RUN_ID",
			Short:   "Show run details",
			Example: "  teleparse runs show run-20260904-101010-abcd1234",
			Args:    cobra.ExactArgs(1),
			RunE:    func(c *cobra.Command, args []string) error { return runsShow(app, c, args[0]) },
		},
		&cobra.Command{
			Use:     "clean",
			Short:   "Delete finished runs from index",
			Example: "  teleparse runs clean --older-than 720h",
			Args:    cobra.NoArgs,
			RunE:    func(c *cobra.Command, _ []string) error { return runsClean(app, c, olderThan) },
		},
	)
	cmd.PersistentFlags().StringVar(&olderThan, "older-than", cleanOlderThanFlagDefault,
		"clean finished runs older than this duration (e.g. 168h)")

	return cmd
}

// runSummaryJSON is the stable json shape of one run row.
type runSummaryJSON struct {
	RunID    string `json:"run_id"`
	Account  string `json:"account"`
	Status   string `json:"status"`
	Started  string `json:"started_at"`
	ResumeAt string `json:"resume_at"`
}

// runSummaryRows projects runs onto shared plain/table rows.
func runSummaryRows(runs []store.Run) ([]string, [][]string) {
	keys := []string{"run_id", "account", "status", "started_at", "resume_at"}

	rows := make([][]string, 0, len(runs))
	for _, run := range runs {
		rows = append(rows, []string{run.RunID, run.Account, run.Status, run.StartedAt, textOrDefault(run.ResumeAt)})
	}

	return keys, rows
}

// renderRunsList prints runs in the requested format.
func renderRunsList(cmd *cobra.Command, runs []store.Run, format OutputFormat) error {
	if format == FormatJSON {
		_, rows := runSummaryRows(runs)

		encoded := make([]runSummaryJSON, 0, len(rows))
		for _, row := range rows {
			encoded = append(encoded, runSummaryJSON{
				RunID: row[0], Account: row[1], Status: row[2], Started: row[3], ResumeAt: row[4],
			})
		}

		return printJSON(cmd, encoded)
	}

	if format == FormatPlain {
		if len(runs) == 0 {
			return printLine(cmd, "no runs\n")
		}

		keys, rows := runSummaryRows(runs)

		return printPlainRows(cmd, keys, rows)
	}

	if len(runs) == 0 {
		return printLine(cmd, "no runs\n")
	}

	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(writer, "RUN\tACCOUNT\tSTATUS\tSTARTED\tRESUME_AT"); err != nil {
		return fmt.Errorf("write runs header: %w", err)
	}

	for _, run := range runs {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			run.RunID, run.Account, run.Status, run.StartedAt, textOrDefault(run.ResumeAt)); err != nil {
			return fmt.Errorf("write run row: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush runs table: %w", err)
	}

	return nil
}

// runDetailJSON is the stable json shape of one full run.
type runDetailJSON struct {
	RunID      string `json:"run_id"`
	Account    string `json:"account"`
	Profile    string `json:"profile"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	ResumeAt   string `json:"resume_at"`
	Error      string `json:"error"`
	FilterJSON string `json:"filter_json"`
}

// renderRunsShow prints one run in the requested format.
func renderRunsShow(cmd *cobra.Command, run store.Run, format OutputFormat) error {
	if format == FormatJSON {
		return printJSON(cmd, runDetailJSON{
			RunID:      run.RunID,
			Account:    run.Account,
			Profile:    textOrDefault(run.Profile),
			Status:     run.Status,
			StartedAt:  run.StartedAt,
			FinishedAt: textOrDefault(run.FinishedAt),
			ResumeAt:   textOrDefault(run.ResumeAt),
			Error:      textOrDefault(run.Error),
			FilterJSON: run.FilterJSON,
		})
	}

	if format == FormatPlain {
		return printPlainPairs(cmd, [][2]string{
			{"run_id", run.RunID},
			{"account", run.Account},
			{"profile", textOrDefault(run.Profile)},
			{"status", run.Status},
			{"started_at", run.StartedAt},
			{"finished_at", textOrDefault(run.FinishedAt)},
			{"resume_at", textOrDefault(run.ResumeAt)},
			{"error", textOrDefault(run.Error)},
		})
	}

	detail := fmt.Sprintf("run id:    %s\naccount:   %s\nprofile:   %s\nstatus:    %s\n"+
		"started:   %s\nfinished:  %s\nresume_at: %s\nerror:     %s\n",
		run.RunID, run.Account, textOrDefault(run.Profile), run.Status,
		run.StartedAt, textOrDefault(run.FinishedAt), textOrDefault(run.ResumeAt), textOrDefault(run.Error))

	return printLine(cmd, "%s\nfilters (toml):\n%s\n", detail, run.FilterJSON)
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

	return renderRunsList(cmd, runs, app.outputFormat())
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

	if err := renderRunsShow(cmd, *run, app.outputFormat()); err != nil {
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
