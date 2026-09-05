package download

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Live UI tuning: refresh cadence, the sliding speed window, how many active
// transfers stay visible before collapsing into an overflow note, the bar
// width, the name column width and the assumed terminal width until the
// first WindowSizeMsg arrives.
const (
	liveRefresh     = 100 * time.Millisecond
	liveSpeedWindow = 3 * time.Second
	maxVisibleItems = 6
	liveBarWidth    = 20
	liveNameWidth   = 24
	liveDefaultWide = 80
)

// speedSample is one point of the cumulative-bytes sliding window used to
// compute transfer speed.
type speedSample struct {
	at    time.Time
	bytes int64
}

// liveItem tracks one in-flight transfer for the live view.
type liveItem struct {
	name    string
	total   int64
	current int64 // resume offset plus freshly written bytes
	samples []speedSample
}

// liveState is the pure, terminal-free model behind the live download view:
// items, counters and rendering are plain functions of the injected clock so
// unit tests drive it deterministically. It is not safe for concurrent use;
// the tea program serializes updates in production.
type liveState struct {
	now           func() time.Time
	started       time.Time
	items         map[string]*liveItem
	order         []string
	done          int64
	skipped       int64
	failed        int64
	retries       int64
	bytes         int64
	phase         string
	throttleNote  string
	throttleUntil time.Time
	total         []speedSample
	failReasons   []string
}

func newLiveState(now func() time.Time) *liveState {
	return &liveState{
		now:     now,
		started: now(),
		items:   map[string]*liveItem{},
	}
}

func (s *liveState) itemStart(key, name string, total, offset int64) {
	if _, exists := s.items[key]; exists {
		return
	}

	item := &liveItem{name: name, total: total, current: offset}

	item.samples = append(item.samples, speedSample{at: s.now(), bytes: offset})

	s.items[key] = item
	s.order = append(s.order, key)
}

func (s *liveState) itemProgress(key string, delta int64) {
	item, ok := s.items[key]
	if !ok || delta <= 0 {
		return
	}

	item.current += delta

	at := s.now()

	item.samples = append(item.samples, speedSample{at: at, bytes: item.current})

	// totalBytes already includes this item's new position.
	s.total = append(s.total, speedSample{at: at, bytes: s.totalBytes()})
}

// failReasonsMax bounds how many distinct failure reasons the live view
// and the final summary surface.
const failReasonsMax = 3

// liveFailMsg settles a transfer as failed and records its reason.
type liveFailMsg struct {
	key    string
	reason string
}

// itemFailed retires the transfer and remembers the failure reason for
// the visible block and the final summary.
func (s *liveState) itemFailed(key, reason string) {
	s.itemDone(key)
	s.failReasons = appendBoundedStrings(s.failReasons, reason, failReasonsMax)
}

// appendBoundedStrings appends distinct reasons, keeping the newest ones.
func appendBoundedStrings(list []string, item string, limit int) []string {
	for idx, existing := range list {
		if existing == item {
			list = append(list[:idx], list[idx+1:]...)

			break
		}
	}

	if len(list) >= limit {
		list = list[len(list)-limit+1:]
	}

	return append(list, item)
}

func (s *liveState) itemDone(key string) {
	if _, ok := s.items[key]; !ok {
		return
	}

	delete(s.items, key)

	for idx, candidate := range s.order {
		if candidate == key {
			s.order = append(s.order[:idx], s.order[idx+1:]...)

			break
		}
	}
}

func (s *liveState) inc(stat string, n int64) {
	switch stat {
	case "downloaded":
		s.done += n
	case "skipped":
		s.skipped += n
	case "failed":
		s.failed += n
	case "retries":
		s.retries += n
	case "bytes":
		s.bytes += n
	}
}

func (s *liveState) setPhase(name string) {
	s.phase = name
}

func (s *liveState) throttled(seconds int) {
	note := "throttled " + strconv.Itoa(seconds) + "s (server flood wait)"

	s.throttleNote, s.throttleUntil = note, s.now().Add(time.Duration(seconds)*time.Second+throttleLinger)
}

// throttleLinger keeps the notice briefly past the slept window so users
// see why a stall happened.
const throttleLinger = 2 * time.Second

// totalBytes is the sum of every tracked item's current position; completed
// transfers leave the map, so this measures in-flight throughput only.
func (s *liveState) totalBytes() int64 {
	var sum int64

	for _, key := range s.order {
		if item, ok := s.items[key]; ok {
			sum += item.current
		}
	}

	return sum
}

// speed derives bytes per second across the sliding sample window.
func speed(samples []speedSample, window time.Duration, now time.Time) float64 {
	if len(samples) < 2 {
		return 0
	}

	first := samples[0]

	for _, sample := range samples[1:] {
		if sample.at.Before(now.Add(-window)) {
			first = sample
		}
	}

	last := samples[len(samples)-1]

	elapsed := last.at.Sub(first.at).Seconds()

	if elapsed <= 0 {
		return 0
	}

	return float64(last.bytes-first.bytes) / elapsed
}

func (s *liveState) lines(width int) []string {
	now := s.now()

	var out []string

	visible := s.order

	if len(visible) > maxVisibleItems {
		visible = visible[:maxVisibleItems]
	}

	for _, key := range visible {
		item, ok := s.items[key]
		if !ok {
			continue
		}

		// Advance the window so idle time decays the reported speed.
		item.samples = append(item.samples, speedSample{at: now, bytes: item.current})

		out = append(out, s.itemLine(item, now, width))
	}

	if hidden := len(s.order) - len(visible); hidden > 0 {
		out = append(out, fmt.Sprintf("... +%d more", hidden))
	}

	out = append(out, s.totalsLine(now))

	for _, reason := range s.failReasons {
		out = append(out, truncateLive("FAIL "+reason, width))
	}

	if s.throttleNote != "" && now.Before(s.throttleUntil) {
		out = append(out, s.throttleNote)
	}

	return out
}

// itemLine renders one active transfer: name, ASCII bar, percent, speed,
// position over total and eta when both are known.
func (s *liveState) itemLine(item *liveItem, now time.Time, width int) string {
	name := truncateLive(item.name, liveNameWidth)

	rate := speed(item.samples, liveSpeedWindow, now)

	parts := []string{name}

	if item.total > 0 {
		parts = append(parts, bar(item.current, item.total), percentOf(item.current, item.total))
	}

	parts = append(parts, humanRate(rate), positionOf(item.current, item.total))

	if remaining := item.total - item.current; item.total > 0 && rate > 0 && remaining > 0 {
		parts = append(parts, "eta "+humanDuration(time.Duration(float64(time.Second)*float64(remaining)/rate)))
	}

	line := joinSpaces(parts)

	if budget := width - liveLinePad; budget > 0 && len(line) > budget {
		line = line[:budget]
	}

	return line
}

// liveLinePad reserves columns before hard-truncating an item line.
const liveLinePad = 4

func (s *liveState) totalsLine(now time.Time) string {
	rate := speed(s.totalSamples(now), liveSpeedWindow, now)

	phase := ""

	if s.phase != "" {
		phase = s.phase + ": "
	}

	return fmt.Sprintf("%s%d done, %d skipped, %d failed, %s at %s",
		phase, s.done, s.skipped, s.failed, humanBytes(s.bytes), humanRate(rate))
}

// totalSamples appends the current in-flight position to the global window.
func (s *liveState) totalSamples(now time.Time) []speedSample {
	return append(s.total, speedSample{at: now, bytes: s.totalBytes()})
}

// liveReasonWidth bounds failure-reason lines in the live view.
const liveReasonWidth = 100

// errString renders an error for the live view (nil-safe).
func errString(err error) string {
	if err == nil {
		return "unknown error"
	}

	return err.Error()
}

func (s *liveState) summaryLine() string {
	line := fmt.Sprintf("%d done (%s), %d skipped, %d failed, %d retries in %s",
		s.done, humanBytes(s.bytes), s.skipped, s.failed, s.retries,
		humanDuration(s.now().Sub(s.started).Round(time.Second)))

	parts := append([]string{line}, prefixedFailures(s.failReasons)...)

	return strings.Join(parts, "")
}

// prefixedFailures renders newline-prefixed failure reasons for the summary.
func prefixedFailures(reasons []string) []string {
	prefixed := make([]string, 0, len(reasons))

	for _, reason := range reasons {
		prefixed = append(prefixed, "\nFAIL "+reason)
	}

	return prefixed
}

// Live reporter messages: every Reporter and ItemReporter call becomes one
// immutable message applied inside the tea update loop.
type (
	liveStatMsg struct {
		stat string
		n    int64
	}
	livePhaseMsg struct{ name string }
	liveStartMsg struct {
		key    string
		name   string
		total  int64
		offset int64
	}
	liveProgressMsg struct {
		key   string
		delta int64
	}
	liveDoneMsg     struct{ key string }
	liveThrottleMsg struct{ seconds int }
	liveTickMsg     time.Time
)

// liveModel adapts liveState onto the bubbletea Model contract.
type liveModel struct {
	state *liveState
	width int
}

func (m liveModel) Init() tea.Cmd {
	return tea.Tick(liveRefresh, func(t time.Time) tea.Msg { return liveTickMsg(t) })
}

func (m liveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:ireturn // the tea Model contract
	switch typed := msg.(type) {
	case liveTickMsg:
		return m, tea.Tick(liveRefresh, func(t time.Time) tea.Msg { return liveTickMsg(t) })
	case tea.WindowSizeMsg:
		m.width = typed.Width

		return m, nil
	case liveStatMsg:
		m.state.inc(typed.stat, typed.n)
	case livePhaseMsg:
		m.state.setPhase(typed.name)
	case liveStartMsg:
		m.state.itemStart(typed.key, typed.name, typed.total, typed.offset)
	case liveProgressMsg:
		m.state.itemProgress(typed.key, typed.delta)
	case liveFailMsg:
		m.state.itemFailed(typed.key, typed.reason)
	case liveDoneMsg:
		m.state.itemDone(typed.key)
	case liveThrottleMsg:
		m.state.throttled(typed.seconds)
	default:
		return m, nil
	}

	return m, nil
}

func (m liveModel) View() tea.View {
	width := m.width

	if width <= 0 {
		width = liveDefaultWide
	}

	return tea.NewView(joinLines(m.state.lines(width)))
}

// LiveReporter renders the uv-style live download view: one line per active
// transfer plus a totals line, refreshed at liveRefresh. It implements
// Reporter and ItemReporter; Close tears the program down and prints the
// final summary line to the same writer.
type LiveReporter struct {
	program *tea.Program
	state   *liveState
	out     io.Writer
}

// NewLiveReporter starts the bubbletea program on out. The caller owns
// calling Close exactly once after the run finishes.
func NewLiveReporter(out io.Writer) *LiveReporter {
	state := newLiveState(time.Now)

	model := liveModel{state: state}

	// TERM_PROGRAM=Apple_Terminal plus TERM=xterm-256color suppress
	// bubbletea's DECRQM capability query (mode 2026/2027): that code path
	// queries ghostty/wezterm-class TERM names unconditionally, and on
	// fast abnormal exits the terminal reply otherwise leaks into the
	// user's shell line. xterm-256color matches none of the query clauses
	// while keeping 256-color output intact.
	program := tea.NewProgram(model,
		tea.WithOutput(out),
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
		tea.WithEnvironment(append(os.Environ(), "TERM_PROGRAM=Apple_Terminal", "TERM=xterm-256color")),
	)

	go func() {
		_, _ = program.Run()
	}()

	return &LiveReporter{program: program, state: state, out: out}
}

// Inc implements Reporter by shipping a counter message.
func (r *LiveReporter) Inc(stat string, n int64) {
	r.program.Send(liveStatMsg{stat: stat, n: n})
}

// SetPhase implements Reporter by shipping a phase message.
func (r *LiveReporter) SetPhase(name string) {
	r.program.Send(livePhaseMsg{name: name})
}

// ItemStart implements ItemReporter by registering a visible transfer.
func (r *LiveReporter) ItemStart(key, name string, total, offset int64) {
	r.program.Send(liveStartMsg{key: key, name: name, total: total, offset: offset})
}

// ItemProgress implements ItemReporter by accumulating written bytes.
func (r *LiveReporter) ItemProgress(key string, delta int64) {
	r.program.Send(liveProgressMsg{key: key, delta: delta})
}

// ItemDone implements ItemReporter by retiring a transfer.
func (r *LiveReporter) ItemDone(key string, failed bool) {
	_ = failed // the live view settles outcomes through the counters

	r.program.Send(liveDoneMsg{key: key})
}

// ItemFailedDetail implements FailureDetailReporter: the live view settles
// failure counts through the counters, so the detail reduces to retiring
// the transfer line.
func (r *LiveReporter) ItemFailedDetail(key string, _ int, err error) {
	r.program.Send(liveFailMsg{key: key, reason: truncateLive(errString(err), liveReasonWidth)})
}

// Throttled implements ItemReporter by surfacing a server flood wait.
func (r *LiveReporter) Throttled(seconds int) {
	r.program.Send(liveThrottleMsg{seconds: seconds})
}

// Close quits the program, waits for the final render and prints the summary.
func (r *LiveReporter) Close() {
	r.program.Quit()

	r.program.Wait()

	_, _ = fmt.Fprintln(r.out, r.state.summaryLine())
}

// QuietReporter prints one line per settled transfer for non-TTY output:
// no live rewriting, no counters, just "done NAME" / "FAIL NAME" lines.
type QuietReporter struct {
	mu    sync.Mutex
	out   io.Writer
	names map[string]string
}

// NewQuietReporter builds the non-TTY reporter writing to out.
func NewQuietReporter(out io.Writer) *QuietReporter {
	return &QuietReporter{out: out, names: map[string]string{}}
}

// Inc implements Reporter; counters surface only in the final summary.
func (q *QuietReporter) Inc(string, int64) {}

// SetPhase implements Reporter; phases are live-UI only.
func (q *QuietReporter) SetPhase(string) {}

// ItemStart implements ItemReporter by remembering the display name.
func (q *QuietReporter) ItemStart(key, name string, _, _ int64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.names[key] = name
}

// ItemProgress implements ItemReporter; byte deltas are live-UI only.
func (q *QuietReporter) ItemProgress(string, int64) {}

// ItemDone implements ItemReporter by printing the settled outcome.
func (q *QuietReporter) ItemDone(key string, failed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	name, known := q.names[key]
	if !known {
		return
	}

	delete(q.names, key)

	prefix := "done"

	if failed {
		prefix = "FAIL"
	}

	fmt.Fprintf(q.out, "%s %s\n", prefix, name) //nolint:errcheck // progress output is best-effort
}

// ItemFailedDetail implements FailureDetailReporter with the terminal
// failure line: the attempts the retry ladder consumed and the last error.
func (q *QuietReporter) ItemFailedDetail(key string, attempts int, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	name, known := q.names[key]
	if !known {
		return
	}

	delete(q.names, key)

	line := fmt.Sprintf("FAIL %s (attempts %d): %v\n", name, attempts, err)

	fmt.Fprint(q.out, line) //nolint:errcheck // progress output is best-effort
}

// Throttled implements ItemReporter; flood waits are visible via parking.
func (q *QuietReporter) Throttled(int) {}
