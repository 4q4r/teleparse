package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	tgapi "github.com/gotd/td/tg"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

// Sentinel errors for resume.
var (
	errRunNotFound     = errors.New("run not found")
	errNothingToResume = errors.New("no parked, failed or interrupted runs to resume")
)

// resumableStatuses are the run states worth resuming: parked by a flood
// wait, failed, or left running by a crash.
func resumableStatuses() map[string]bool {
	return map[string]bool{
		store.StatusParked:  true,
		store.StatusFailed:  true,
		store.StatusRunning: true,
	}
}

func resumeCmd(app *App) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "resume [RUN_ID]",
		Short:   "Resume parked (FloodWait) or crashed runs",
		Example: "  teleparse resume\n  teleparse resume run-20260904-101010-abcd1234\n  teleparse resume --all",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runResume(app, c, args, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "resume every parked, failed or interrupted run")

	return cmd
}

func runResume(app *App, cmd *cobra.Command, args []string, all bool) error {
	state, err := openStore(app)
	if err != nil {
		return fail(cmd, err)
	}

	defer func() { _ = state.Close() }()

	reset, err := state.ResetDownloading(cmd.Context())
	if err != nil {
		return fail(cmd, fmt.Errorf("requeue interrupted rows: %w", err))
	}

	if reset > 0 {
		if err := printLine(cmd, "requeued %d interrupted row(s)\n", reset); err != nil {
			return fail(cmd, err)
		}
	}

	runs, err := resumableRuns(cmd.Context(), state, args, all)
	if err != nil {
		return fail(cmd, err)
	}

	for _, run := range runs {
		if err := resumeOne(app, cmd, run); err != nil {
			return fail(cmd, err)
		}
	}

	return nil
}

func resumableRuns(ctx context.Context, state *store.Store, args []string, all bool) ([]store.Run, error) {
	if !all && len(args) == 1 {
		run, exists, err := state.GetRun(ctx, args[0])
		if err != nil {
			return nil, fmt.Errorf("load run %s: %w", args[0], err)
		}

		if !exists {
			return nil, fmt.Errorf("run %s: %w", args[0], errRunNotFound)
		}

		return []store.Run{*run}, nil
	}

	listed, err := state.ListRuns(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	resumable := resumableStatuses()

	var runs []store.Run

	for _, run := range listed {
		if !resumable[run.Status] {
			continue
		}

		runs = append(runs, run)

		if !all && len(args) == 0 {
			return runs[:1], nil // newest resumable run by default
		}
	}

	if len(runs) == 0 {
		return nil, errNothingToResume
	}

	return runs, nil
}

// resumeOne re-runs the stored pipeline on its original account under a new
// run id; the walk re-discovers rows idempotently (done rows are skipped by
// the manager) and re-walking is also what repopulates file references.
func resumeOne(app *App, cmd *cobra.Command, run store.Run) error {
	deadline, mustWait, err := resumeDeadline(run)
	if err != nil {
		return err
	}

	if mustWait {
		return printLine(cmd, "run %s: wait until %s before resuming\n",
			run.RunID, deadline.Format(time.RFC3339))
	}

	var payload runPayload

	if err := toml.Unmarshal([]byte(run.FilterJSON), &payload); err != nil {
		return fmt.Errorf("decode stored filters of run %s: %w", run.RunID, err)
	}

	opts := payload.Options

	plan, err := filters.Compile(&opts)
	if err != nil {
		return fmt.Errorf("compile stored filters of run %s: %w", run.RunID, err)
	}

	if err := printLine(cmd, "resuming %s (account %s, %d chat spec(s))\n",
		run.RunID, run.Account, len(payload.Chats)); err != nil {
		return err
	}

	cfg := *app.cfg
	cfg.Auth.Account = run.Account

	creds, err := app.resolveCreds()
	if err != nil {
		return err
	}

	runErr := tg.Run(cmd.Context(), run.Account, creds, &cfg, app.paths, func(
		ctx context.Context,
		client *telegram.Client,
	) error {
		// Cache-only premium resolution (no RPC before the session
		// opens): an unknown answer defaults the export cap to the base
		// 2GiB; see runAccountSession.
		premium := tg.NewAccountManager(app.paths.AccountsDir).
			AccountPremium(ctx, run.Account, nil)

		return withAPI(ctx, client, cfg.Net.Takeout, premium.Premium, func(ctx context.Context, api *tgapi.Client) error {
			return executeRun(ctx, cmd, app, run.Account, textOrDefault(run.Profile),
				opts, plan, payload.Chats, runMode{}, api, client, cfg.Net.Takeout,
				takeoutFileCap(cfg.Net.Takeout, premium.Premium))
		}, func(finishErr error) {
			if app.silentMode(cmd) {
				return
			}

			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", app.errStyle.Warning(
				"takeout session finished with a server warning (ignored): "+finishErr.Error()))
		})
	})
	if runErr != nil {
		return fmt.Errorf("resume run %s: %w", run.RunID, runErr)
	}

	return nil
}

// resumeDeadline reports when a parked run may resume and whether that
// moment is still in the future.
func resumeDeadline(run store.Run) (time.Time, bool, error) {
	if run.ResumeAt == nil {
		return time.Time{}, false, nil
	}

	deadline, err := time.Parse(time.RFC3339Nano, *run.ResumeAt)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse resume_at of run %s: %w", run.RunID, err)
	}

	return deadline, time.Until(deadline) > 0, nil
}
