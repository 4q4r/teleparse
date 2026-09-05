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
// durations feed the ETA average, the trail depth of recently scanned
// chats, the title truncation width and the clock scale factors.
const (
	scanRefresh    = 100 * time.Millisecond
	scanEtaWindow  = 12
	scanTrailLen   = 2
	scanTitleWidth = 36
	scanSecsPerMin = 60
	scanSecsPerHor = 3600
)

// scanTrailEntry is one recently completed chat in the trail.
type scanTrailEntry struct {
	title   string
	matches int
}

// scanState is the pure, terminal-free model behind the live scan walk
// view: counters, per-chat durations and the recent-chat trail are plain
// functions of the injected clock, so unit tests drive it
// deterministically. It is not safe for concurrent use; the tea program
// serializes updates in production.
type scanState struct {
	now       func() time.Time
	started   time.Time
	total     int
	done      int
	matched   int64
	durations []time.Duration
	trail     []scanTrailEntry
}

func newScanState(now func() time.Time, total int) *scanState {
	return &scanState{now: now, started: now(), total: total}
}

// chatDone records one completed walk: its match count and duration. The
// duration feeds the ETA moving average; the chat joins the trail.
func (s *scanState) chatDone(title string, matches int, took time.Duration) {
	s.done++
	s.matched += int64(matches)
	s.durations = appendBounded(s.durations, took, scanEtaWindow)
	s.trail = appendBoundedTrail(s.trail, scanTrailEntry{title: title, matches: matches})

	if len(s.durations) > scanEtaWindow {
		s.durations = s.durations[len(s.durations)-scanEtaWindow:]
	}
}

// appendBounded appends item evicting the oldest entry past limit.
func appendBounded[T any](slice []T, item T, limit int) []T {
	if len(slice) >= limit {
		slice = slice[1:]
	}

	return append(slice, item)
}

// appendBoundedTrail keeps the trail growing from scratch and never
// evicting below its own length; the newest entry renders last.
func appendBoundedTrail(trail []scanTrailEntry, entry scanTrailEntry) []scanTrailEntry {
	if len(trail) >= scanTrailLen {
		trail = trail[len(trail)-scanTrailLen+1:]
	}

	return append(trail, entry)
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

// lines renders the live block: a head line with counters, elapsed time
// and the ETA, plus the last scanTrailLen completed chats (newest last).
// The ETA segment disappears once the walk finishes.
func (s *scanState) lines(styler Styler) []string {
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

	out := make([]string, 0, 1+len(s.trail))
	out = append(out, strings.Join(head, "  "))

	for idx, entry := range s.trail {
		out = append(out, s.trailLine(styler, entry, idx == len(s.trail)-1))
	}

	return out
}

// progressText renders the done/total counter.
func (s *scanState) progressText() string {
	return strconv.Itoa(s.done) + "/" + strconv.Itoa(s.total)
}

// trailLine renders one trail entry: an ASCII tree prefix, the truncated
// title and the green match count.
func (s *scanState) trailLine(styler Styler, entry scanTrailEntry, newest bool) string {
	prefix := "|- "
	if newest {
		prefix = "`- "
	}

	return styler.Dim(prefix) + truncateScanTitle(entry.title) + ": " +
		styler.Success(strconv.Itoa(entry.matches)+" matches")
}

// Live scan messages: each completed walk becomes one immutable message
// applied inside the tea update loop; ticks keep the clock fields fresh.
type (
	scanChatMsg struct {
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
		return m, tea.Tick(scanRefresh, func(t time.Time) tea.Msg { return scanTickMsg(t) })
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

// newScanProgress picks the scan progress surface: the live bubbletea
// block when stderr is a terminal (--no-ascii forces the line surface
// even on terminals, the block repaints with ANSI escapes), one line per
// chat otherwise, nothing at all under -s or with no targets.
func newScanProgress(cmd *cobra.Command, app *App, total int) *scanProgress {
	if app == nil || total == 0 || app.silentMode(cmd) {
		return nil
	}

	if !app.noASCII && term.IsTerminal(int(os.Stderr.Fd())) {
		state := newScanState(time.Now, total)

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
