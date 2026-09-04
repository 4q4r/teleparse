package filters_test

import (
	"teleparse/internal/filters"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileFilePredicates(t *testing.T) {
	t.Parallel()

	videoCtx := withFile(baseContext(), videoFile(10_000_000))

	mixedCaseVideoCtx := withFile(baseContext(), videoFile(10_000_000))
	mixedCaseVideoCtx.File.Mime = "Video/MP4"

	pdfCtx := withFile(baseContext(), documentFile("report.pdf", ".pdf", "application/pdf", 5_000))

	zipCtx := withFile(baseContext(), documentFile("backup.zip", ".zip", "application/zip", 20_000_000))

	dotlessExtCtx := withFile(baseContext(), documentFile("backup.zip", "zip", "application/zip", 100))

	upperExtCtx := withFile(baseContext(), documentFile("Backup.ZIP", ".ZIP", "application/zip", 100))

	textCtx := baseContext()

	absentFileCtx := baseContext()
	absentFileCtx.File = &filters.FileInfo{Kind: "document", Name: "ghost.bin"}

	mutedCtx := withFile(baseContext(), videoFile(10_000_000))
	mutedCtx.File.Streamable = false
	mutedCtx.File.NoSound = true

	zeroDimCtx := withFile(baseContext(), videoFile(10_000_000))
	zeroDimCtx.File.Width = 0
	zeroDimCtx.File.Height = 0

	const size20MB = 20_000_000

	runPredicateCases(t, []predCase{
		{
			name: "mime glob matches case insensitive",
			set:  func(o *filters.Options) { o.Mime = []string{"video/*"} },
			ctx:  mixedCaseVideoCtx, want: true,
		},
		{
			name: "mime glob rejects other kind",
			set:  func(o *filters.Options) { o.Mime = []string{"video/*"} },
			ctx:  pdfCtx, want: false,
		},
		{
			name: "mime list matches any entry",
			set:  func(o *filters.Options) { o.Mime = []string{"audio/*", "application/zip"} },
			ctx:  zipCtx, want: true,
		},
		{
			name: "mime filter rejects fileless message",
			set:  func(o *filters.Options) { o.Mime = []string{"application/zip"} },
			ctx:  textCtx, want: false,
		},
		{
			name: "ext normalizes leading dot",
			set:  func(o *filters.Options) { o.Ext = []string{".zip"} },
			ctx:  dotlessExtCtx, want: true,
		},
		{
			name: "ext normalizes case",
			set:  func(o *filters.Options) { o.Ext = []string{".zip"} },
			ctx:  upperExtCtx, want: true,
		},
		{
			name: "ext rejects other extension",
			set:  func(o *filters.Options) { o.Ext = []string{".rar"} },
			ctx:  zipCtx, want: false,
		},
		{
			name: "name glob matches filename",
			set:  func(o *filters.Options) { o.NameGlob = "*.zip" },
			ctx:  zipCtx, want: true,
		},
		{
			name: "name glob rejects filename",
			set:  func(o *filters.Options) { o.NameGlob = "*.rar" },
			ctx:  zipCtx, want: false,
		},
		{
			name: "name regex matches filename",
			set:  func(o *filters.Options) { o.NameRegex = `^report_` },
			ctx:  pdfCtx, want: false,
		},
		{
			name: "name regex rejects filename",
			set:  func(o *filters.Options) { o.NameRegex = `^memo_` },
			ctx:  pdfCtx, want: false,
		},
		{
			name: "min size inclusive at lower bound",
			set:  func(o *filters.Options) { o.MinSize = "20MB" },
			ctx:  withFile(baseContext(), videoFile(size20MB)), want: true,
		},
		{
			name: "min size rejects below bound",
			set:  func(o *filters.Options) { o.MinSize = "20MB" },
			ctx:  videoCtx, want: false,
		},
		{
			name: "max size inclusive at upper bound",
			set:  func(o *filters.Options) { o.MaxSize = "20MB" },
			ctx:  withFile(baseContext(), videoFile(size20MB)), want: true,
		},
		{
			name: "max size rejects above bound",
			set:  func(o *filters.Options) { o.MaxSize = "20MB" },
			ctx:  withFile(baseContext(), videoFile(size20MB+1)), want: false,
		},
		{
			name: "size filter rejects fileless message",
			set:  func(o *filters.Options) { o.MinSize = "1MB" },
			ctx:  textCtx, want: false,
		},
		{
			name: "size filter rejects non present file",
			set:  func(o *filters.Options) { o.MinSize = "1MB" },
			ctx:  absentFileCtx, want: false,
		},
		{
			name: "min duration inclusive at bound",
			set:  func(o *filters.Options) { o.MinDuration = "120s" },
			ctx:  videoCtx, want: true,
		},
		{
			name: "min duration rejects shorter file",
			set:  func(o *filters.Options) { o.MinDuration = "121s" },
			ctx:  videoCtx, want: false,
		},
		{
			name: "max duration inclusive at bound",
			set:  func(o *filters.Options) { o.MaxDuration = "2m" },
			ctx:  videoCtx, want: true,
		},
		{
			name: "max duration rejects longer file",
			set:  func(o *filters.Options) { o.MaxDuration = "119s" },
			ctx:  videoCtx, want: false,
		},
		{
			name: "duration filter rejects fileless message",
			set:  func(o *filters.Options) { o.MinDuration = "10s" },
			ctx:  textCtx, want: false,
		},
		{
			name: "min width inclusive at bound",
			set:  func(o *filters.Options) { o.MinWidth = 1920 },
			ctx:  videoCtx, want: true,
		},
		{
			name: "min width rejects narrower file",
			set:  func(o *filters.Options) { o.MinWidth = 1921 },
			ctx:  videoCtx, want: false,
		},
		{
			name: "max width inclusive at bound",
			set:  func(o *filters.Options) { o.MaxWidth = 1920 },
			ctx:  videoCtx, want: true,
		},
		{
			name: "max width rejects wider file",
			set:  func(o *filters.Options) { o.MaxWidth = 1919 },
			ctx:  videoCtx, want: false,
		},
		{
			name: "min height inclusive at bound",
			set:  func(o *filters.Options) { o.MinHeight = 1080 },
			ctx:  videoCtx, want: true,
		},
		{
			name: "max height rejects taller file",
			set:  func(o *filters.Options) { o.MaxHeight = 1079 },
			ctx:  videoCtx, want: false,
		},
		{
			name: "min mp passes two megapixel file",
			set:  func(o *filters.Options) { o.MinMP = 2.0 },
			ctx:  videoCtx, want: true,
		},
		{
			name: "min mp rejects smaller file",
			set:  func(o *filters.Options) { o.MinMP = 2.0 },
			ctx:  withFile(baseContext(), photoFile(1_000)), want: false,
		},
		{
			name: "min mp rejects unknown dimensions",
			set:  func(o *filters.Options) { o.MinMP = 0.5 },
			ctx:  zeroDimCtx, want: false,
		},
		{
			name: "min mp rejects fileless message",
			set:  func(o *filters.Options) { o.MinMP = 0.5 },
			ctx:  textCtx, want: false,
		},
		{
			name: "streamable true matches streamable file",
			set:  func(o *filters.Options) { o.Streamable = filters.Tri(true) },
			ctx:  videoCtx, want: true,
		},
		{
			name: "streamable true rejects non streamable file",
			set:  func(o *filters.Options) { o.Streamable = filters.Tri(true) },
			ctx:  mutedCtx, want: false,
		},
		{
			name: "streamable false matches non streamable file",
			set:  func(o *filters.Options) { o.Streamable = filters.Tri(false) },
			ctx:  mutedCtx, want: true,
		},
		{
			name: "streamable tri rejects fileless message",
			set:  func(o *filters.Options) { o.Streamable = filters.Tri(false) },
			ctx:  textCtx, want: false,
		},
		{
			name: "no sound true matches muted file",
			set:  func(o *filters.Options) { o.NoSound = filters.Tri(true) },
			ctx:  mutedCtx, want: true,
		},
		{
			name: "no sound false rejects muted file",
			set:  func(o *filters.Options) { o.NoSound = filters.Tri(false) },
			ctx:  mutedCtx, want: false,
		},
	})
}

func TestCompileFileRegexMatches(t *testing.T) {
	t.Parallel()

	reportCtx := withFile(baseContext(), documentFile("report_2026.pdf", ".pdf", "application/pdf", 5_000))

	opts := &filters.Options{NameRegex: `^report_\d{4}\.pdf$`}
	plan, err := filters.Compile(opts)
	require.NoError(t, err)
	assert.True(t, planMatches(plan, reportCtx))
	assert.False(t, planMatches(plan, withFile(baseContext(), documentFile("memo.pdf", ".pdf", "application/pdf", 5_000))))
}
