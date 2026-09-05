package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Live scan tuning: the repaint cadence, how many recent per-chat walk
// durations feed the ETA average and the title truncation width.
const (
	scanRefresh    = 100 * time.Millisecond
	scanEtaWindow  = 12
	scanTitleWidth = 36
	scanSecsPerMin = 60
	scanSecsPerHor = 3600
)

// scanSpinners holds the spinner frame sets: braille on unicode terminals
// (the uv look), ASCII fallback under --no-ascii.
var scanSpinners = struct{ braille, ascii []string }{ //nolint:gochecknoglobals // immutable frame tables
	braille: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	ascii:   []string{"|", "/", "-", "\\"},
}

// scanDoneEntry is one settled chat line, kept forever (uv-style: finished
// items persist and scroll into the terminal history).
type scanDoneEntry struct {
	title   string
	matches int
}

// scanState is the pure, terminal-free model behind the live scan view:
// settled chat lines accumulate top-down, the chat currently being walked
// renders with a spinner, and the counters footer stays at the bottom like
// uv's status line. Everything is a function of the injected clock, so
// unit tests drive it deterministically. Not safe for concurrent use; the
// tea program serializes updates in production.
type scanState struct {
	now       func() time.Time
	started   time.Time
	total     int
	done      int
	matched   int64
	durations []time.Duration
	current   string
	spinner   int
	asciiOnly bool
	settled   []scanDoneEntry
}

func newScanState(now func() time.Time, total int, asciiOnly bool) *scanState {
	return &scanState{now: now, started: now(), total: total, asciiOnly: asciiOnly}
}

// chatStart marks a chat as the one being walked right now.
func (s *scanState) chatStart(title string) {
	s.current = title
}

// chatDone records one completed walk: it settles the chat's line,
// advances the counters and feeds the ETA moving average.
func (s *scanState) chatDone(title string, matches int, took time.Duration) {
	s.done++
	s.matched += int64(matches)
	s.durations = appendBounded(s.durations, took, scanEtaWindow)
	s.settled = append(s.settled, scanDoneEntry{title: title, matches: matches})

	if s.current == title {
		s.current = ""
	}
}

// appendBounded appends item evicting the oldest entry past limit.
func appendBounded[T any](slice []T, item T, limit int) []T {
	if len(slice) >= limit {
		slice = slice[1:]
	}

	return append(slice, item)
}

// eta projects the remaining walk time from the moving average of recent
// per-chat durations; known is false before any chat settles or when the
// walk already finished.
func (s *scanState) eta() (time.Duration, bool) {
	remaining := s.total - s.done

	if remaining <= 0 || len(s.durations) == 0 {
		return 0, false
	}

	var sum time.Duration

	for _, sample := range s.durations {
		sum += sample
	}

	return sum / time.Duration(len(s.durations)) * time.Duration(remaining), true
}

// lines renders the uv-style block: settled chat lines ("+ Title (N)"),
// the spinner line for the chat being walked, and the counters footer
// (scanning X/Y, matched, elapsed, ETA) pinned last.
func (s *scanState) lines(styler Styler) []string {
	out := make([]string, 0, len(s.settled)+2)

	for _, entry := range s.settled {
		out = append(out, styler.Success("+")+" "+truncateScanTitle(entry.title)+" ("+
			styler.Success(strconv.Itoa(entry.matches))+")")
	}

	if s.current != "" && s.done < s.total {
		out = append(out, styler.Dim(s.spin())+" "+truncateScanTitle(s.current))
	}

	out = append(out, s.footer(styler))

	return out
}

// footer renders the bottom status line: progress, matches, elapsed, ETA.
func (s *scanState) footer(styler Styler) string {
	head := []string{
		styler.Dim("scanning") + " " + styler.Success(s.progressText()),
		styler.Dim("matched") + " " + styler.Success(strconv.FormatInt(s.matched, 10)+" files"),
		scanElapsed(s.now().Sub(s.started)),
	}

	if eta, known := s.eta(); known {
		head = append(head, styler.Dim("ETA")+" "+scanClock(eta))
	} else if s.done < s.total {
		head = append(head, styler.Dim("ETA")+" --")
	}

	return strings.Join(head, "  ")
}

// spin renders the current spinner frame for the active chat line.
func (s *scanState) spin() string {
	frames := scanSpinners.braille
	if s.asciiOnly {
		frames = scanSpinners.ascii
	}

	return frames[s.spinner%len(frames)]
}

// progressText renders the done/total counter.
func (s *scanState) progressText() string {
	return strconv.Itoa(s.done) + "/" + strconv.Itoa(s.total)
}

// Live scan messages: chat starts and completions are immutable messages
// applied inside the tea update loop; ticks animate the spinner and keep
// the clock fresh.
type (
	scanStartMsg struct{ title string }
	scanChatMsg  struct {
		title   string
		matches int
		took    time.Duration
	}
	scanTickMsg time.Time
)

// scanModel adapts scanState onto the bubbletea Model contract.
type scanModel struct {
	state  *scanState
	styler Styler
}

func (m scanModel) Init() tea.Cmd {
	return tea.Tick(scanRefresh, func(t time.Time) tea.Msg { return scanTickMsg(t) })
}

func (m scanModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:ireturn // the tea Model contract
	switch typed := msg.(type) {
	case scanTickMsg:
		m.state.spinner++

		return m, tea.Tick(scanRefresh, func(t time.Time) tea.Msg { return scanTickMsg(t) })
	case scanStartMsg:
		m.state.chatStart(typed.title)
	case scanChatMsg:
		m.state.chatDone(typed.title, typed.matches, typed.took)
	default:
		return m, nil
	}

	return m, nil
}

func (m scanModel) View() tea.View {
	return tea.NewView(strings.Join(m.state.lines(m.styler), "\n"))
}

// scanProgress is the walk-loop progress surface: a bubbletea program on
// stderr for terminals, one plain line per chat otherwise. The zero
// construction is invalid; build it with newScanProgress. All methods are
// nil-safe so call sites can skip the silent/empty checks.
type scanProgress struct {
	program *tea.Program
	out     io.Writer
	styler  Styler
	total   int
	done    int
	tty     bool
}

// newScanProgress picks the scan progress surface: the uv-style live view
// when stderr is a terminal (--no-ascii forces the line surface — the
// block repaints with ANSI and shows a braille spinner otherwise), one
// line per chat otherwise, nothing at all under -s or with no targets.
func newScanProgress(cmd *cobra.Command, app *App, total int) *scanProgress {
	if app == nil || total == 0 || app.silentMode(cmd) {
		return nil
	}

	if !app.noASCII && term.IsTerminal(int(os.Stderr.Fd())) {
		state := newScanState(time.Now, total, app.noASCII)

		program := tea.NewProgram(scanModel{state: state, styler: app.errStyle},
			tea.WithOutput(cmd.ErrOrStderr()),
			tea.WithInput(nil),
			tea.WithoutSignalHandler(),
		)

		go func() {
			_, _ = program.Run()
		}()

		return &scanProgress{program: program, tty: true, total: total}
	}

	return &scanProgress{out: cmd.ErrOrStderr(), styler: app.errStyle, total: total}
}

// chatStart reports that the walk of title began.
func (p *scanProgress) chatStart(title string) {
	if p == nil || !p.tty {
		return
	}

	p.program.Send(scanStartMsg{title: title})
}

// chatDone reports one completed walk; took is the wall-clock walk time.
func (p *scanProgress) chatDone(title string, matches int, took time.Duration) {
	if p == nil {
		return
	}

	if p.tty {
		p.program.Send(scanChatMsg{title: title, matches: matches, took: took})

		return
	}

	p.done++

	line := fmt.Sprintf("scanned %d/%d  %s: %s\n",
		p.done, p.total, title, p.styler.Success(strconv.Itoa(matches)+" matches"))

	_, _ = fmt.Fprint(p.out, line)
}

// close tears the live program down; a no-op for the line surface.
func (p *scanProgress) close() {
	if p == nil || !p.tty {
		return
	}

	p.program.Quit()
	p.program.Wait()
}

// truncateScanTitle shortens title to scanTitleWidth, marking truncation
// with a tilde.
func truncateScanTitle(title string) string {
	if len(title) <= scanTitleWidth {
		return title
	}

	return title[:scanTitleWidth-1] + "~"
}

// scanClock renders whole-second durations compactly with zero-padded
// minutes and seconds ("4s", "1m04s", "1h02m03s").
func scanClock(d time.Duration) string {
	total := int(d.Seconds())

	hours := total / scanSecsPerHor
	minutes := total % scanSecsPerHor / scanSecsPerMin
	seconds := total % scanSecsPerMin

	switch {
	case hours > 0:
		return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	default:
		return strconv.Itoa(seconds) + "s"
	}
}

// scanElapsed renders the live elapsed time: tenths of a second under a
// minute ("12.3s"), scanClock above.
func scanElapsed(elapsed time.Duration) string {
	if elapsed < time.Minute {
		return fmt.Sprintf("%.1fs", elapsed.Seconds())
	}

	return scanClock(elapsed)
}
