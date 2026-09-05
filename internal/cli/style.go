package cli

import (
	"charm.land/lipgloss/v2"
)

// StyleOptions collects the inputs of the color decision. Colors survive
// only when stdout is a terminal and none of --no-color, --no-ascii or
// NO_COLOR asked for plain output (--no-ascii implies plain: the systemd
// and docker scenarios want zero control sequences).
type StyleOptions struct {
	NoColorFlag bool
	NoASCIIFlag bool
	NoColorEnv  bool
	IsTTY       bool
}

// Enabled resolves the matrix: true only when every gate passes.
func (o StyleOptions) Enabled() bool {
	return o.IsTTY && !o.NoColorFlag && !o.NoASCIIFlag && !o.NoColorEnv
}

// Styler renders colored CLI fragments. The zero value (and every Styler
// built disabled) passes text through unchanged, so tests and pipes see
// plain output; an enabled Styler wraps text in ANSI colors. Styles are
// constructed per Styler, never shared as globals.
type Styler struct {
	enabled bool
	err     lipgloss.Style
	ok      lipgloss.Style
	warn    lipgloss.Style
	dim     lipgloss.Style
}

// NewStyler builds a Styler; disabled stylers render plain text.
func NewStyler(enabled bool) Styler {
	if !enabled {
		return Styler{}
	}

	return Styler{
		enabled: true,
		err:     lipgloss.NewStyle().Foreground(lipgloss.Red),
		ok:      lipgloss.NewStyle().Foreground(lipgloss.Green),
		warn:    lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		dim:     lipgloss.NewStyle().Faint(true),
	}
}

// Enabled reports whether colors are active.
func (s Styler) Enabled() bool {
	return s.enabled
}

// ErrorPrefix renders the "error:" label used in front of failure messages.
func (s Styler) ErrorPrefix() string {
	return s.err.Render("error:")
}

// Success renders success fragments (the green "+" result prefix).
func (s Styler) Success(text string) string {
	return s.ok.Render(text)
}

// Failure renders failure fragments (doctor FAIL).
func (s Styler) Failure(text string) string {
	return s.err.Render(text)
}

// Warning renders attention fragments (doctor fix hints).
func (s Styler) Warning(text string) string {
	return s.warn.Render(text)
}

// Dim renders de-emphasized fragments (hints, sources, SKIP).
func (s Styler) Dim(text string) string {
	return s.dim.Render(text)
}

// doctorBadge renders a doctor check status word with its semantic color:
// PASS green, FAIL red, everything else dim.
func doctorBadge(styler Styler, status string) string {
	switch status {
	case "PASS":
		return styler.Success(status)
	case "FAIL":
		return styler.Failure(status)
	default:
		return styler.Dim(status)
	}
}
