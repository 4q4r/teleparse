package filters

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors for size/time parsing.
var (
	ErrEmptySize       = errors.New("empty size")
	ErrBadSizeNumber   = errors.New("bad number in size")
	ErrNegativeSize    = errors.New("negative size")
	ErrUnknownSizeUnit = errors.New("unknown unit (use B,KB,MB,GB,KiB,MiB,GiB)")
	ErrEmptyTime       = errors.New("empty time")
	ErrEmptyRelative   = errors.New("empty relative time")
	ErrBadRelativeNum  = errors.New("expected number")
	ErrBadRelativeUnit = errors.New("bad unit")
	ErrNonPositive     = errors.New("must be positive")
	ErrBadTimeFormat   = errors.New("need ISO 8601 or relative like 7d/12h/30m")
)

const (
	hoursPerDay    = 24
	daysPerWeek    = 7
	daysPerYear    = 365
	secondsPerHour = 3600
)

// unitTable maps size suffixes to multipliers (checked longest-first).
func unitTable() []struct {
	suffix string
	mul    int64
} {
	return []struct {
		suffix string
		mul    int64
	}{
		{"kib", 1 << 10},
		{"mib", 1 << 20},
		{"gib", 1 << 30},
		{"tib", 1 << 40},
		{"kb", 1e3},
		{"mb", 1e6},
		{"gb", 1e9},
		{"tb", 1e12},
		{"k", 1e3},
		{"m", 1e6},
		{"g", 1e9},
		{"t", 1e12},
		{"b", 1},
	}
}

// timeLayouts lists accepted absolute timestamp layouts (longest-first).
func timeLayouts() []string {
	return []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"}
}

// ParseSize parses humanized byte sizes: "500", "10KB", "20MB", "1.5GiB", "2G".
// Units are case-insensitive; bare numbers are bytes.
func ParseSize(input string) (int64, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return 0, ErrEmptySize
	}

	lower := strings.ToLower(text)

	for _, unit := range unitTable() {
		if !strings.HasSuffix(lower, unit.suffix) {
			continue
		}

		return parseSizeNumber(text, lower[:len(lower)-len(unit.suffix)], unit.mul)
	}

	return parseSizeNumber(text, lower, 1)
}

func parseSizeNumber(orig, numText string, mul int64) (int64, error) {
	numText = strings.TrimSpace(numText)
	if numText == "" {
		numText = "1"
	}

	value, err := strconv.ParseFloat(numText, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w %q: %w", orig, ErrBadSizeNumber, numText, err)
	}

	if value < 0 {
		return 0, fmt.Errorf("size %q: %w", orig, ErrNegativeSize)
	}

	return int64(value * float64(mul)), nil
}

// ParseTime parses an absolute ISO 8601 timestamp/date or a relative offset
// like "7d", "12h", "30m" (ago). Absolute wins when it parses cleanly.
func ParseTime(input string) (time.Time, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return time.Time{}, ErrEmptyTime
	}

	for _, layout := range timeLayouts() {
		if t, err := time.Parse(layout, text); err == nil {
			return t, nil
		}
	}

	d, err := ParseRelative(text)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q: %w: %w", text, ErrBadTimeFormat, err)
	}

	return time.Now().Add(-d), nil
}

// ParseRelative parses "7d", "12h", "30m", "90s", "1h30m", "2w" into a duration.
func ParseRelative(input string) (time.Duration, error) {
	text := strings.TrimSpace(strings.ToLower(input))
	if text == "" {
		return 0, ErrEmptyRelative
	}

	if d, err := time.ParseDuration(text); err == nil {
		return d, nil
	}

	return parseRelativeCompound(text)
}

func parseRelativeCompound(text string) (time.Duration, error) {
	var total time.Duration

	rest := text
	for pos := 0; pos < len(rest); {
		numEnd := scanDigits(rest, pos)
		if numEnd == pos {
			return 0, fmt.Errorf("relative %q: %w at %d", text, ErrBadRelativeNum, pos)
		}

		num, err := strconv.Atoi(rest[pos:numEnd])
		if err != nil {
			return 0, fmt.Errorf("relative %q: %w: %w", text, ErrBadParse, err)
		}

		unitEnd := scanUnit(rest, numEnd)

		d, err := relativeUnitDuration(num, rest[numEnd:unitEnd])
		if err != nil {
			return 0, fmt.Errorf("relative %q: %w %q", text, err, rest[numEnd:unitEnd])
		}

		total += d
		pos = unitEnd
	}

	if total <= 0 {
		return 0, fmt.Errorf("relative %q: %w", text, ErrNonPositive)
	}

	return total, nil
}

func scanDigits(text string, from int) int {
	end := from
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}

	return end
}

func scanUnit(text string, from int) int {
	end := from
	for end < len(text) && (text[end] < '0' || text[end] > '9') && text[end] != '.' {
		end++
	}

	return end
}

func relativeUnitDuration(num int, unit string) (time.Duration, error) {
	switch unit {
	case "d":
		return time.Duration(num) * hoursPerDay * time.Hour, nil
	case "w":
		return time.Duration(num) * daysPerWeek * hoursPerDay * time.Hour, nil
	case "y":
		return time.Duration(num) * daysPerYear * hoursPerDay * time.Hour, nil
	default:
		d, err := time.ParseDuration(strconv.Itoa(num) + unit)
		if err != nil {
			return 0, fmt.Errorf("%q: %w: %w", strconv.Itoa(num)+unit, ErrBadParse, err)
		}

		return d, nil
	}
}
