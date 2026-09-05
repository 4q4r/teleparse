// Package export renders store media rows as JSONL and CSV for the
// teleparse export commands.
package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/4q4r/teleparse/internal/store"
)

// decimalBase is the strconv base used for integer formatting.
const decimalBase = 10

// ExportJSONL writes rows as one JSON object per line; no BOM is emitted.
func ExportJSONL(w io.Writer, rows []store.MediaRow) error {
	for idx := range rows {
		encoded, err := json.Marshal(&rows[idx])
		if err != nil {
			return fmt.Errorf("marshal row %d/%d/%d: %w",
				rows[idx].ChatID, rows[idx].MessageID, rows[idx].MediaIndex, err)
		}

		if _, err := w.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("write jsonl row: %w", err)
		}
	}

	return nil
}

// ExportCSV writes rows as RFC 4180 CSV with a header line in the stable
// csvHeader order; no BOM is emitted.
func ExportCSV(w io.Writer, rows []store.MediaRow) error {
	writer := csv.NewWriter(w)

	if err := writer.Write(csvHeader()); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	for idx := range rows {
		if err := writer.Write(csvRow(&rows[idx])); err != nil {
			return fmt.Errorf("stage csv row %d/%d/%d: %w",
				rows[idx].ChatID, rows[idx].MessageID, rows[idx].MediaIndex, err)
		}
	}

	writer.Flush()

	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}

	return nil
}

func csvHeader() []string {
	return []string{
		"chat_id", "message_id", "media_index", "media_class", "media_id",
		"mime", "size", "date", "sender_id", "filename", "grouped_id",
		"status", "path", "sha256",
	}
}

func csvRow(row *store.MediaRow) []string {
	return []string{
		strconv.FormatInt(row.ChatID, decimalBase),
		strconv.FormatInt(row.MessageID, decimalBase),
		strconv.Itoa(row.MediaIndex),
		row.MediaClass,
		strconv.FormatInt(row.MediaID, decimalBase),
		textOrEmpty(row.Mime),
		int64OrEmpty(row.Size),
		textOrEmpty(row.Date),
		int64OrEmpty(row.SenderID),
		textOrEmpty(row.Filename),
		int64OrEmpty(row.GroupedID),
		row.Status,
		textOrEmpty(row.Path),
		textOrEmpty(row.Sha256),
	}
}

func textOrEmpty(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func int64OrEmpty(value *int64) string {
	if value == nil {
		return ""
	}

	return strconv.FormatInt(*value, decimalBase)
}
