package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/4q4r/teleparse/internal/store"
	"github.com/4q4r/teleparse/internal/verify"

	"github.com/spf13/cobra"
)

// errVerifyProblems is the sentinel wrapped by every failing sweep so
// callers can distinguish "damage found" from tool failure.
var errVerifyProblems = errors.New("integrity problems found")

// verifyCmd sweeps the manifest against the downloads tree, fully offline.
func verifyCmd(app *App) *cobra.Command {
	var deep, fix bool

	cmd := &cobra.Command{
		Use:   "verify [CHATS]...",
		Short: "Verify downloaded media integrity against the manifest",
		Long: "Walks the media manifest offline and classifies every row: ok, missing,\n" +
			"final-missing-blob-alive, size-mismatch, hash-mismatch, format-error,\n" +
			"orphan-blob; queued or failed rows without a path are counted but skipped.\n" +
			"--deep adds sha256 re-hashing and native format validation using only the\n" +
			"Go standard library: zip and tar.gz/gz are fully verified (per-member CRC32),\n" +
			"rar gets an honest lite check (signature plus best-effort end marker).\n" +
			"--fix relinks finals from surviving blobs, requeues damaged rows for\n" +
			"re-download and gc's orphaned blobs through the dedupe path.\n" +
			"Exits non-zero when problems remain after the run.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerify(app, cmd, args, deep, fix)
		},
	}

	cmd.Flags().BoolVar(&deep, "deep", false,
		"re-hash recorded sha256 digests and natively validate zip/tar.gz/gz\n"+
			"(rar: signature check only)")
	cmd.Flags().BoolVar(&fix, "fix", false,
		"repair: relink finals from live blobs, requeue damaged rows, gc orphaned blobs")

	return cmd
}

// runVerify resolves the chat filter, sweeps, renders and derives the exit
// grade from what remains.
func runVerify(app *App, cmd *cobra.Command, args []string, deep, fix bool) error {
	state, err := openStore(app)
	if err != nil {
		return err
	}

	defer func() { _ = state.Close() }()

	chats, err := resolveVerifyChats(cmd.Context(), state, args)
	if err != nil {
		return fail(cmd, err)
	}

	opts := verify.Options{
		Root:      app.paths.Downloads,
		Deep:      deep,
		Fix:       fix,
		Chats:     chats,
		OnProblem: verifyProblemPrinter(cmd, app),
	}

	report, err := verify.Sweep(cmd.Context(), state, opts)
	if err != nil {
		return fail(cmd, err)
	}

	if err := renderVerify(cmd, app, report); err != nil {
		return fail(cmd, err)
	}

	if report.Remaining > 0 {
		return remainingError(report, fix)
	}

	return nil
}

// remainingError renders the failing exit with the actionable hint.
func remainingError(report *verify.Report, fix bool) error {
	hint := "run with --fix to repair"
	if fix {
		hint = "re-run teleparse dl for requeued rows; format errors need manual attention"
	}

	return fmt.Errorf("verify: %d problem(s) remain (%s): %w", report.Remaining, hint, errVerifyProblems)
}

// resolveVerifyChats turns the positional filter args into chat ids:
// numeric args match chat ids directly, anything else matches stored chat
// titles as a case-insensitive substring; an absent filter or "all" walks
// every chat.
func resolveVerifyChats(ctx context.Context, state *store.Store, args []string) ([]int64, error) {
	if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "all")) {
		return nil, nil
	}

	var (
		ids    []int64
		titles []string
	)

	for _, arg := range args {
		if id, err := strconv.ParseInt(arg, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)

			continue
		}

		titles = append(titles, strings.ToLower(arg))
	}

	if len(titles) == 0 {
		return ids, nil
	}

	chatIDs, err := state.DistinctChatIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}

	for _, chatID := range chatIDs {
		if err := matchChatTitle(ctx, state, chatID, titles, &ids); err != nil {
			return nil, err
		}
	}

	return ids, nil
}

// matchChatTitle appends the chat when any title needle matches its
// stored title.
func matchChatTitle(ctx context.Context, state *store.Store, chatID int64, titles []string, ids *[]int64) error {
	title, ok, err := state.ChatTitle(ctx, chatID)
	if err != nil {
		return fmt.Errorf("chat title %d: %w", chatID, err)
	}

	if !ok {
		return nil
	}

	lower := strings.ToLower(title)

	for _, needle := range titles {
		if strings.Contains(lower, needle) {
			*ids = append(*ids, chatID)

			return nil
		}
	}

	return nil
}

// verifyProblemPrinter streams one settled line per problem finding to
// stderr, colored dim, silent under --silent.
func verifyProblemPrinter(cmd *cobra.Command, app *App) func(verify.Finding) {
	if app.silentMode(cmd) {
		return nil
	}

	return func(finding verify.Finding) {
		line := app.errStyle.Dim("verify:") + " " + string(finding.Class) + " " + finding.Path

		if finding.Fixed != "" {
			line += " " + app.errStyle.Dim("("+finding.Fixed+")")
		}

		// stderr write failures cannot be surfaced from the sweep
		// callback; the report and exit code still carry the damage.
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), line) // best-effort progress line
	}
}

// renderVerify prints the report in the requested output format.
func renderVerify(cmd *cobra.Command, app *App, report *verify.Report) error {
	switch app.outputFormat() {
	case FormatJSON:
		return printJSON(cmd, report)
	case FormatPlain:
		return renderVerifyPlain(cmd, report)
	default:
		return renderVerifyTable(cmd, report)
	}
}

// renderVerifyTable prints the classification counts, the problem rows
// and, after --fix, the repair summary.
func renderVerifyTable(cmd *cobra.Command, report *verify.Report) error {
	if err := printVerifyCounts(cmd, report); err != nil {
		return err
	}

	if len(report.Findings) > 0 {
		if err := printVerifyFindings(cmd, report); err != nil {
			return err
		}
	}

	return printVerifyFooter(cmd, report)
}

// printVerifyCounts renders the CLASS ROWS table including the two
// off-manifest counters (pathless rows and orphaned blobs).
func printVerifyCounts(cmd *cobra.Command, report *verify.Report) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(writer, "CLASS\tROWS"); err != nil {
		return fmt.Errorf("write class header: %w", err)
	}

	ordered := verifyClassOrder(report)

	for _, class := range ordered {
		if _, err := fmt.Fprintf(writer, "%s\t%d\n", class.class, class.rows); err != nil {
			return fmt.Errorf("write class row: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush class table: %w", err)
	}

	return printLine(cmd, "\n")
}

// classCount is one rendered CLASS ROWS line.
type classCount struct {
	class string
	rows  int
}

// verifyClassOrder stabilizes the class table order and appends the
// pathless and orphan counters.
func verifyClassOrder(report *verify.Report) []classCount {
	ordered := []classCount{
		{class: string(verify.ClassOK)},
		{class: string(verify.ClassMissing)},
		{class: string(verify.ClassFinalMissingBlobAlive)},
		{class: string(verify.ClassSizeMismatch)},
		{class: string(verify.ClassHashMismatch)},
		{class: string(verify.ClassFormatError)},
		{class: string(verify.ClassUncheckedFormat)},
		{class: string(verify.ClassOrphanBlob), rows: report.Orphaned},
		{class: string(verify.ClassRowWithoutPath), rows: report.SkippedNoPath},
	}

	for idx := range ordered[:len(ordered)-2] {
		ordered[idx].rows = report.Classes[verify.Class(ordered[idx].class)]
	}

	out := make([]classCount, 0, len(ordered))

	for _, row := range ordered {
		if row.rows > 0 {
			out = append(out, row)
		}
	}

	return out
}

// printVerifyFindings renders the problem table: one row per finding,
// orphans included with a dash in the chat columns.
func printVerifyFindings(cmd *cobra.Command, report *verify.Report) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	if _, err := fmt.Fprintln(writer, "CHAT\tMSG\tCLASS\tFILE\tDETAIL"); err != nil {
		return fmt.Errorf("write finding header: %w", err)
	}

	for _, finding := range report.Findings {
		chat, msg := "-", "-"
		if finding.ChatID != 0 {
			chat = strconv.FormatInt(finding.ChatID, 10)
			msg = strconv.FormatInt(finding.MessageID, 10)
		}

		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			chat, msg, finding.Class, verifyFindingFile(finding), verifyFindingDetail(finding)); err != nil {
			return fmt.Errorf("write finding row: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush finding table: %w", err)
	}

	return printLine(cmd, "\n")
}

// verifyFindingFile picks the display name of one finding.
func verifyFindingFile(finding verify.Finding) string {
	if finding.Filename != "" {
		return finding.Filename
	}

	return filepath.Base(finding.Path)
}

// verifyFindingDetail merges the detail and the repair action.
func verifyFindingDetail(finding verify.Finding) string {
	if finding.Fixed == "" {
		return finding.Detail
	}

	if finding.Detail == "" {
		return finding.Fixed
	}

	return finding.Detail + " -> " + finding.Fixed
}

// printVerifyFooter prints the totals and repair summary.
func printVerifyFooter(cmd *cobra.Command, report *verify.Report) error {
	if err := printLine(cmd, "problems: %d, remaining: %d\n", report.Problems, report.Remaining); err != nil {
		return err
	}

	if report.Repair.Relinked+report.Repair.Requeued > 0 || report.Repair.GcRemoved > 0 {
		return printLine(cmd, "fixed: relinked %d, requeued %d, gc removed %d (%s freed)\n",
			report.Repair.Relinked, report.Repair.Requeued,
			report.Repair.GcRemoved, humanBytes(report.Repair.GcFreedBytes))
	}

	return nil
}

// verifyPlainPairBase reserves room for the fixed header pairs plus the
// trailing totals.
const verifyPlainPairBase = 6

// renderVerifyPlain prints greppable key: value lines.
func renderVerifyPlain(cmd *cobra.Command, report *verify.Report) error {
	pairs := make([][2]string, 0, verifyPlainPairBase)
	pairs = append(pairs,
		[][2]string{
			{"rows", strconv.Itoa(report.Rows)},
			{"skipped_no_path", strconv.Itoa(report.SkippedNoPath)},
			{"blobs", strconv.Itoa(report.Blobs)},
			{"orphaned", strconv.Itoa(report.Orphaned)},
		}...,
	)

	for _, class := range verifyClassOrder(report) {
		pairs = append(pairs, [2]string{"class." + class.class, strconv.Itoa(class.rows)})
	}

	pairs = append(pairs,
		[][2]string{
			{"problems", strconv.Itoa(report.Problems)},
			{"remaining", strconv.Itoa(report.Remaining)},
		}...,
	)

	if err := printPlainPairs(cmd, pairs); err != nil {
		return fmt.Errorf("write plain verify totals: %w", err)
	}

	for _, finding := range report.Findings {
		if err := printPlainVerifyFinding(cmd, finding); err != nil {
			return err
		}
	}

	return nil
}

// printPlainVerifyFinding renders one problem block in plain format.
func printPlainVerifyFinding(cmd *cobra.Command, finding verify.Finding) error {
	block := [][2]string{
		{"class", string(finding.Class)},
		{"path", finding.Path},
	}

	if finding.Filename != "" {
		block = append(block, [2]string{"filename", finding.Filename})
	}

	if finding.Detail != "" {
		block = append(block, [2]string{"detail", finding.Detail})
	}

	if finding.Fixed != "" {
		block = append(block, [2]string{"fixed", finding.Fixed})
	}

	if err := printPlainPairs(cmd, block); err != nil {
		return fmt.Errorf("write plain finding: %w", err)
	}

	return printLine(cmd, "\n")
}
