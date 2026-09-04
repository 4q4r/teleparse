package download_test

import (
	"strings"
	"teleparse/internal/download"
	"teleparse/internal/filters"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func templateContext() filters.Context {
	return filters.Context{
		Chat:   filters.Chat{ID: 42, Type: "private", Title: "News/Feed"},
		Sender: filters.Sender{Present: true, ID: 7, Name: "Alice Smith"},
		Message: filters.Message{
			ID:   1234,
			Date: time.Date(2026, time.September, 4, 15, 4, 5, 0, time.UTC).Unix(),
		},
		File: &filters.FileInfo{
			Present: true,
			Kind:    "document",
			Name:    "report..zip",
			Ext:     ".zip",
			Size:    10,
			Mime:    "application/zip",
		},
	}
}

func TestRenderTemplatePlaceholders(t *testing.T) {
	t.Parallel()

	fctx := templateContext()

	// {filename} sanitizes: path separators and control runes collapse, edges trim.
	got, err := download.RenderTemplate("{chat}/{sender}/{filename}", fctx, fctx.File)
	require.NoError(t, err)
	assert.Equal(t, "News_Feed/Alice Smith/report..zip", got)
}

func TestRenderTemplateDateFormats(t *testing.T) {
	t.Parallel()

	fctx := templateContext()

	cases := map[string]string{
		"{date}":              "2026-09-04",
		"{date:YYYY}":         "2026",
		"{date:YYYY-MM}":      "2026-09",
		"{date:YYYY-MM-DD}":   "2026-09-04",
		"{date:DD-HH-mm-ss}":  "04-15-04-05",
		"{chat}/{date:%Y-%m}": "News_Feed/2026-09",
	}
	for template, want := range cases {
		got, err := download.RenderTemplate(template, fctx, fctx.File)
		require.NoError(t, err, "template %q", template)
		assert.Equal(t, want, got, "template %q", template)
	}
}

func TestRenderTemplateMsgidAndExt(t *testing.T) {
	t.Parallel()

	fctx := templateContext()

	got, err := download.RenderTemplate("{msgid}{ext}", fctx, fctx.File)
	require.NoError(t, err)
	assert.Equal(t, "1234.zip", got)
}

func TestRenderTemplateFilenameFallback(t *testing.T) {
	t.Parallel()

	fctx := templateContext()
	fctx.File.Name = ""

	got, err := download.RenderTemplate("{filename}", fctx, fctx.File)
	require.NoError(t, err)
	assert.Equal(t, "file_1234", got)
}

func TestRenderTemplateFilenameSanitization(t *testing.T) {
	t.Parallel()

	fctx := templateContext()
	fctx.File.Name = "../../etc/passwd\x00"

	got, err := download.RenderTemplate("{filename}", fctx, fctx.File)
	require.NoError(t, err)
	assert.Equal(t, "etc_passwd", got)
}

func TestRenderTemplateNoSender(t *testing.T) {
	t.Parallel()

	fctx := templateContext()
	fctx.Sender = filters.Sender{}

	got, err := download.RenderTemplate("{sender}/{filename}", fctx, fctx.File)
	require.NoError(t, err)
	assert.Equal(t, "unknown/report..zip", got)
}

func TestRenderTemplateUnknownPlaceholderFails(t *testing.T) {
	t.Parallel()

	fctx := templateContext()

	_, err := download.RenderTemplate("{bogus}", fctx, fctx.File)
	require.ErrorIs(t, err, download.ErrBadTemplate)
	assert.Contains(t, err.Error(), "bogus")
}

func TestRenderTemplateUnknownDateTokenFails(t *testing.T) {
	t.Parallel()

	fctx := templateContext()

	_, err := download.RenderTemplate("{date:ZZ}", fctx, fctx.File)
	require.ErrorIs(t, err, download.ErrBadTemplate)
}

func TestRenderTemplateRejectsTraversal(t *testing.T) {
	t.Parallel()

	fctx := templateContext()

	for _, template := range []string{"../{filename}", "a/../../b/{filename}", "/abs/{filename}"} {
		_, err := download.RenderTemplate(template, fctx, fctx.File)
		require.ErrorIs(t, err, download.ErrUnsafePath, "template %q", template)
	}
}

func TestRenderTemplateFilenameTruncation(t *testing.T) {
	t.Parallel()

	fctx := templateContext()
	fctx.File.Name = strings.Repeat("a", 300)

	got, err := download.RenderTemplate("{filename}", fctx, fctx.File)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(got), 200)
}

func TestSanitizeFilenameFallback(t *testing.T) {
	t.Parallel()

	fctx := templateContext()
	fctx.File.Name = "..."
	fctx.Message.ID = 99

	got, err := download.RenderTemplate("{filename}", fctx, fctx.File)
	require.NoError(t, err)
	assert.Equal(t, "file_99", got)
}
