// Package download implements the teleparse download pipeline: path
// templating, a paced worker pool with .part resume, collision policies,
// sidecar metadata, post-download hooks and flood-wait parking.
package download

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"teleparse/internal/filters"
	"time"
	"unicode/utf8"
)

// Sentinel errors wrapped by dynamic template failures.
var (
	ErrBadTemplate = errors.New("invalid path template")
	ErrUnsafePath  = errors.New("template result escapes the output root")
)

// filenameMaxBytes caps the rendered {filename} component.
const filenameMaxBytes = 200

// dateToken is one strptime-style token mapped onto the Go reference layout.
type dateToken struct {
	spec   string
	layout string
}

// dateTokens maps strptime-ish tokens onto Go reference-layout fragments,
// longest specs first so the scanner never mis-splits a token.
func dateTokens() []dateToken {
	return []dateToken{
		{"YYYY", "2006"},
		{"YY", "06"},
		{"MM", "01"},
		{"DD", "02"},
		{"HH", "15"},
		{"mm", "04"},
		{"ss", "05"},
		{"%Y", "2006"},
		{"%y", "06"},
		{"%m", "01"},
		{"%d", "02"},
		{"%H", "15"},
		{"%M", "04"},
		{"%S", "05"},
	}
}

// RenderTemplate renders a relative output path from the message context.
// Placeholders: {chat} (sanitized title), {sender}, {msgid}, {filename}
// (sanitized, fallback file_<msgid>), {ext}, {date} (YYYY-MM-DD) and
// {date:FMT} where FMT combines the tokens YYYY, YY, MM, DD, HH, mm, ss (or
// the %Y %y %m %d %H %M %S forms) with non-letter separators such as - or _.
// Unknown placeholders, unknown date tokens and results that would escape the
// output root (absolute paths or ".." components) fail loudly.
func RenderTemplate(template string, fctx filters.Context, file *filters.FileInfo) (string, error) {
	var out strings.Builder

	rest := template

	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			out.WriteString(rest)

			break
		}

		closed := strings.IndexByte(rest[open:], '}')
		if closed < 0 {
			return "", fmt.Errorf("%q: %w: unterminated placeholder", template, ErrBadTemplate)
		}

		closed += open

		out.WriteString(rest[:open])

		token := rest[open+1 : closed]

		rendered, err := renderToken(token, fctx, file)
		if err != nil {
			return "", err
		}

		out.WriteString(rendered)

		rest = rest[closed+1:]
	}

	result := out.String()
	if err := assertSafePath(result); err != nil {
		return "", err
	}

	return result, nil
}

func renderToken(token string, fctx filters.Context, file *filters.FileInfo) (string, error) {
	name, format, hasFormat := strings.Cut(token, ":")
	// A single trailing colon means an empty format; treat as bare name.
	if hasFormat && format == "" {
		hasFormat = false
	}

	switch name {
	case "chat":
		return sanitizeSegment(fctx.Chat.Title, "chat_"+strconv.FormatInt(fctx.Chat.ID, 10)), nil
	case "sender":
		return senderLabel(fctx), nil
	case "msgid":
		return strconv.FormatInt(fctx.Message.ID, 10), nil
	case "filename":
		return sanitizeFilename(fileName(file), fctx.Message.ID), nil
	case "ext":
		return fileExt(file), nil
	case "date":
		return renderDate(format, fctx.Message.Date, hasFormat)
	default:
		return "", fmt.Errorf("{%s}: %w: unknown placeholder", token, ErrBadTemplate)
	}
}

func renderDate(format string, unix int64, hasFormat bool) (string, error) {
	layout := "2006-01-02"

	if hasFormat {
		converted, err := convertDateTokens(format)
		if err != nil {
			return "", err
		}

		layout = converted
	}

	return time.Unix(unix, 0).UTC().Format(layout), nil
}

// convertDateTokens rewrites strptime-style tokens into a Go reference
// layout; separators (any non-letter, non-percent rune) pass through
// unchanged, while unrecognized letters fail loudly.
func convertDateTokens(format string) (string, error) {
	var out strings.Builder

	for idx := 0; idx < len(format); {
		matched := false

		for _, token := range dateTokens() {
			if strings.HasPrefix(format[idx:], token.spec) {
				out.WriteString(token.layout)
				idx += len(token.spec)
				matched = true

				break
			}
		}

		if matched {
			continue
		}

		head, size := utf8.DecodeRuneInString(format[idx:])
		if isLetter(head) {
			return "", fmt.Errorf("%q: %w: unknown date token at %d", format, ErrBadTemplate, idx)
		}

		out.WriteRune(head)

		idx += size
	}

	return out.String(), nil
}

// assertSafePath rejects absolute results and any ".." path component.
func assertSafePath(result string) error {
	if result == "" {
		return fmt.Errorf("%q: %w: empty", result, ErrUnsafePath)
	}

	if strings.HasPrefix(result, "/") || strings.Contains(result, `\`) {
		return fmt.Errorf("%q: %w: absolute or backslash path", result, ErrUnsafePath)
	}

	for _, part := range strings.Split(result, "/") {
		if part == ".." {
			return fmt.Errorf("%q: %w: parent traversal", result, ErrUnsafePath)
		}
	}

	return nil
}

// sanitizeSegment makes one path component out of a free-form label such as
// a chat title, falling back when nothing usable remains.
func sanitizeSegment(label, fallback string) string {
	clean := sanitizeRunes(label)
	if clean == "" {
		return fallback
	}

	return clean
}

func senderLabel(fctx filters.Context) string {
	if !fctx.Sender.Present {
		return "unknown"
	}

	return sanitizeSegment(strings.TrimSpace(fctx.Sender.Name), "unknown")
}

func fileName(file *filters.FileInfo) string {
	if file == nil {
		return ""
	}

	return file.Name
}

func fileExt(file *filters.FileInfo) string {
	if file == nil {
		return ""
	}

	return file.Ext
}

// sanitizeFilename applies sanitizeRunes and caps the result at
// filenameMaxBytes on a rune boundary, falling back to file_<msgid>.
func sanitizeFilename(name string, msgID int64) string {
	clean := sanitizeRunes(name)
	if clean == "" {
		return "file_" + strconv.FormatInt(msgID, 10)
	}

	if len(clean) <= filenameMaxBytes {
		return clean
	}

	truncated := clean[:filenameMaxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}

	return truncated
}

// sanitizeRunes replaces path separators and control runes with underscores,
// then trims leading and trailing dots, underscores and spaces so no
// traversal or hidden-component games survive.
func sanitizeRunes(text string) string {
	var out strings.Builder

	out.Grow(len(text))

	for _, char := range text {
		switch {
		case char == '/' || char == '\\':
			out.WriteByte('_')
		case char < ' ' || char == 0x7f:
			out.WriteByte('_')
		default:
			out.WriteRune(char)
		}
	}

	return strings.Trim(out.String(), "._")
}

func isLetter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}
