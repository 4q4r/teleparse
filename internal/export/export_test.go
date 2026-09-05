package export_test

import (
	"bytes"
	"testing"

	"github.com/4q4r/teleparse/internal/export"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func sampleRows() []store.MediaRow {
	return []store.MediaRow{
		{
			ChatID:     1,
			MessageID:  10,
			MediaIndex: 0,
			MediaClass: "photo",
			MediaID:    555,
			Mime:       ptr("image/jpeg"),
			Size:       ptr(int64(1024)),
			Date:       ptr("2026-09-01T10:00:00Z"),
			SenderID:   ptr(int64(42)),
			Status:     store.StatusDone,
			Path:       ptr("out/a.jpg"),
			Sha256:     ptr("abc123"),
		},
		{
			ChatID:     2,
			MessageID:  11,
			MediaIndex: 0,
			MediaClass: "document",
			MediaID:    666,
			Filename:   ptr(`report, "final".csv`),
			Status:     store.StatusQueued,
		},
	}
}

func TestExportJSONLGolden(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	require.NoError(t, export.ExportJSONL(&buf, sampleRows()))

	want := `{"chat_id":1,"message_id":10,"media_index":0,"media_class":"photo","media_id":555,` +
		`"mime":"image/jpeg","size":1024,"date":"2026-09-01T10:00:00Z","sender_id":42,` +
		`"status":"done","path":"out/a.jpg","sha256":"abc123"}` + "\n" +
		`{"chat_id":2,"message_id":11,"media_index":0,"media_class":"document","media_id":666,` +
		`"filename":"report, \"final\".csv","status":"queued"}` + "\n"
	assert.Equal(t, want, buf.String())
}

func TestExportCSVGolden(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	require.NoError(t, export.ExportCSV(&buf, sampleRows()))

	want := "chat_id,message_id,media_index,media_class,media_id,mime,size,date,sender_id," +
		"filename,grouped_id,status,path,sha256\n" +
		"1,10,0,photo,555,image/jpeg,1024,2026-09-01T10:00:00Z,42,,,done,out/a.jpg,abc123\n" +
		"2,11,0,document,666,,,,,\"report, \"\"final\"\".csv\",,queued,,\n"
	assert.Equal(t, want, buf.String())
	assert.NotContains(t, buf.String()[:1], "\ufeff", "no BOM may be emitted")
}

func TestExportEmpty(t *testing.T) {
	t.Parallel()

	var jsonl bytes.Buffer
	require.NoError(t, export.ExportJSONL(&jsonl, nil))
	assert.Empty(t, jsonl.String())

	var csvOut bytes.Buffer
	require.NoError(t, export.ExportCSV(&csvOut, nil))
	assert.Equal(t,
		"chat_id,message_id,media_index,media_class,media_id,mime,size,date,sender_id,"+
			"filename,grouped_id,status,path,sha256\n",
		csvOut.String())
}
