package download

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// liveSizeBase and its powers drive human-readable byte formatting; 1024
// throughout, matching the rest of the CLI.
const liveSizeBase = 1024.0

// sizeUnitsLive pairs divisors with labels, largest first.
type sizeUnitLive struct {
	divisor float64
	label   string
}

func liveSizeUnits() []sizeUnitLive {
	return []sizeUnitLive{
		{liveSizeBase * liveSizeBase * liveSizeBase, "GiB"},
		{liveSizeBase * liveSizeBase, "MiB"},
		{liveSizeBase, "KiB"},
	}
}

// humanBytes renders a byte count as B/KiB/MiB/GiB.
func humanBytes(value int64) string {
	scaled := float64(value)

	for _, unit := range liveSizeUnits() {
		if scaled >= unit.divisor {
			return fmt.Sprintf("%.1f%s", scaled/unit.divisor, unit.label)
		}
	}

	return strconv.FormatInt(value, 10) + "B"
}

// humanRate renders bytes per second.
func humanRate(rate float64) string {
	if rate < 1 {
		return "0B/s"
	}

	return humanBytes(int64(rate)) + "/s"
}

// positionOf renders "cur/total", or just "cur" when the total is unknown.
func positionOf(current, total int64) string {
	if total <= 0 {
		return humanBytes(current)
	}

	return humanBytes(current) + "/" + humanBytes(total)
}

// percentOf renders the completed share of total, clamped to [0%, 100%]:
// over-counted deltas from any accounting hiccup must never render beyond
// the total.
func percentOf(current, total int64) string {
	if total <= 0 {
		return ""
	}

	if current > total {
		current = total
	}

	if current < 0 {
		current = 0
	}

	return strconv.FormatInt(current*livePercentMax/total, 10) + "%"
}

// livePercentMax is the percent scale used by percentOf.
const livePercentMax = 100

// bar renders an ASCII progress bar of liveBarWidth cells.
func bar(current, total int64) string {
	if total <= 0 {
		return ""
	}

	filled := int(current * int64(liveBarWidth) / total)

	if filled < 0 {
		filled = 0
	}

	if filled > liveBarWidth {
		filled = liveBarWidth
	}

	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", liveBarWidth-filled) + "]"
}

// truncateLive shortens name to width, marking truncation with a tilde.
func truncateLive(name string, width int) string {
	if len(name) <= width {
		return name
	}

	return name[:width-1] + "~"
}

// truncateLiveTail shortens text to width keeping the TAIL, marking
// truncation with a leading tilde: Telegram errors end with their machine
// code, so the tail carries the actionable part of a failure reason.
func truncateLiveTail(text string, width int) string {
	if len(text) <= width {
		return text
	}

	if width < 2 {
		return "~"
	}

	return "~" + text[len(text)-width+1:]
}

// humanDuration renders whole seconds compactly ("4s", "1m30s").
func humanDuration(d time.Duration) string {
	return d.Round(time.Second).Truncate(time.Second).String()
}

func joinSpaces(parts []string) string {
	filtered := make([]string, 0, len(parts))

	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}

	return strings.Join(filtered, " ")
}

// joinLines stitches rendered view lines with newlines.
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
