package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"teleparse/internal/export"
	"teleparse/internal/store"

	"github.com/spf13/cobra"
)

// errRunFilterUnsupported reports the honest state-schema limit: media rows
// carry no run linkage, so export cannot filter by run.
var errRunFilterUnsupported = errors.New("filtering by run is not supported: media rows carry no run linkage")

// exportFilePerm is the optional --out target permission.
const exportFilePerm = 0o600

func exportCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export manifest (jsonl/csv)",
	}

	for _, format := range []struct {
		name string
		run  func(cmd *cobra.Command, st *store.Store, chatID int64, out string) error
	}{
		{"jsonl", exportRows(export.ExportJSONL)},
		{"csv", exportRows(export.ExportCSV)},
	} {
		cmd.AddCommand(exportSub(app, format.name, format.run))
	}

	return cmd
}

func exportSub(app *App, name string, run func(*cobra.Command, *store.Store, int64, string) error) *cobra.Command {
	var (
		chatID int64
		out    string
		runID  string
	)

	cmd := &cobra.Command{
		Use:     name,
		Short:   "Export manifest as " + name,
		Example: "  teleparse export " + name + " --chat 123456789 --out manifest." + name,
		Args:    cobra.NoArgs,
		RunE: func(sub *cobra.Command, _ []string) error {
			if runID != "" {
				return fail(sub, fmt.Errorf("--run %s: %w", runID, errRunFilterUnsupported))
			}

			state, err := openStore(app)
			if err != nil {
				return fail(sub, err)
			}

			defer func() { _ = state.Close() }()

			return run(sub, state, chatID, out)
		},
	}
	cmd.Flags().Int64Var(&chatID, "chat", 0, "restrict export to one chat id")
	cmd.Flags().StringVar(&out, "out", "", "write to this file instead of stdout")
	cmd.Flags().StringVar(&runID, "run", "", "restrict export to one run (unsupported by the state schema)")

	return cmd
}

// exportRows binds one export writer to the command surface.
func exportRows(write func(dest io.Writer, rows []store.MediaRow) error) func(
	*cobra.Command, *store.Store, int64, string,
) error {
	return func(cmd *cobra.Command, state *store.Store, chatID int64, out string) error {
		rows, err := state.ListMediaRows(cmd.Context(), chatID)
		if err != nil {
			return fail(cmd, fmt.Errorf("list media rows: %w", err))
		}

		if out == "" {
			if err := write(cmd.OutOrStdout(), rows); err != nil {
				return fail(cmd, err)
			}

			return nil
		}

		file, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, exportFilePerm)
		if err != nil {
			return fail(cmd, fmt.Errorf("open export target %s: %w", out, err))
		}

		defer func() { _ = file.Close() }()

		if err := write(file, rows); err != nil {
			return fail(cmd, err)
		}

		return nil
	}
}
