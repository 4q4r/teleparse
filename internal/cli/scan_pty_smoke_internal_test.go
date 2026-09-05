package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// termIsTTY reports whether stderr is a terminal.
func termIsTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// TestScanProgressPTYSmoke renders the scan progress model across a fake
// 254-chat walk and writes the first and last frames (with ANSI codes) to
// $TELEPARSE_PTY_FRAMES when set, for visual verification under a real
// PTY (script -qec "go test ./internal/cli -run TestScanProgressPTYSmoke").
func TestScanProgressPTYSmoke(t *testing.T) {
	framesPath := os.Getenv("TELEPARSE_PTY_FRAMES")
	if framesPath == "" {
		t.Skip("set TELEPARSE_PTY_FRAMES to capture frames")
	}

	if !termIsTTY() {
		t.Fatal("stderr must be a terminal for the PTY smoke")
	}

	styler := NewStyler(true)

	state := newScanState(elapsedScanClock(90*time.Second), 254, false)
	model := scanModel{state: state, styler: styler}

	// First frame: one chat done, ETA already meaningful.
	model.Update(scanChatMsg{title: "News Channel", matches: 37, took: 2 * time.Second})

	first := model.View().Content

	// Walk the remaining 253 chats with varied durations and counts.
	for idx := range 253 {
		title := fmt.Sprintf("Chat %03d", idx+2)
		matches := (idx * 7) % 23

		model.Update(scanChatMsg{title: title, matches: matches, took: 2100 * time.Millisecond})
	}

	last := model.View().Content

	frames := make([]string, 0, 6)
	frames = append(frames,
		"--- first frame (1/254 chats done) ---", first,
		"--- last frame (254/254 done) ---", last)

	out, err := os.Create(framesPath)
	if err != nil {
		t.Fatalf("create frames file: %v", err)
	}

	defer func() { _ = out.Close() }()

	for _, frame := range frames {
		if _, err := fmt.Fprintln(out, frame); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}

	plain := stripANSIFrames(last)
	if !strings.Contains(plain, "scanning 254/254") || !strings.Contains(plain, "matched") {
		t.Fatalf("last frame missing totals: %q", last)
	}

	// Final count-only table and summary the way previewRun renders them
	// right after the live block tears down.
	collector := &walkCollector{}
	targets := make([]scan.Target, 0, 6)

	for idx, count := range []int{128, 37, 0, 12, 0, 4} {
		targets = append(targets, scanTargetFixture(idx, count))

		for range count {
			collector.items = append(collector.items, store.MediaItem{ChatID: int64(idx + 1)})
		}
	}

	table := &strings.Builder{}
	tableCmd := &cobra.Command{}
	tableCmd.SetOut(table)

	if err := printCounts(tableCmd, &App{style: styler}, collector, targets, 0); err != nil {
		t.Fatalf("render counts: %v", err)
	}

	errCmd := &cobra.Command{}
	errCmd.SetErr(framesErrWriter{out})

	if err := printScanSummary(errCmd, &App{errStyle: styler}, len(targets), len(collector.items), 0, 72*time.Second); err != nil {
		t.Fatalf("render summary: %v", err)
	}

	frames = append(frames, "--- final count-only table (stdout) ---", strings.TrimSuffix(table.String(), "\n"))

	if _, err := fmt.Fprintln(out); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	for _, frame := range frames[len(frames)-2:] {
		if _, err := fmt.Fprintln(out, frame); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
}

// framesErrWriter forwards the summary line into the frames capture.
type framesErrWriter struct{ out *os.File }

func (w framesErrWriter) Write(p []byte) (int, error) { return w.out.Write(p) }

// scanTargetFixture builds one scan.Target with a titled chat.
func scanTargetFixture(idx, matches int) scan.Target {
	return scan.Target{Chat: filters.Chat{ID: int64(idx + 1), Title: fmt.Sprintf("Chat %02d (%d hits)", idx+1, matches)}}
}

// framesANSI matches the SGR escape sequences lipgloss emits.
var framesANSI = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSIFrames removes ANSI escapes so content assertions stay
// color-agnostic.
func stripANSIFrames(text string) string {
	return framesANSI.ReplaceAllString(text, "")
}

// elapsedScanClock returns a clock frozen at start for the construction
// call, then reporting start+total so elapsed renders as total.
func elapsedScanClock(total time.Duration) func() time.Time {
	start := time.Unix(0, 0)
	first := true

	return func() time.Time {
		if first {
			first = false

			return start
		}

		return start.Add(total)
	}
}
