package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// errBadFormat reports a --format value outside the supported set.
var errBadFormat = errors.New("must be one of table|json|plain")

// OutputFormat selects the rendering of command output.
type OutputFormat string

// Supported output formats: table (padded, the default), json (stable
// field names) and plain (greppable key: value lines, no padding).
const (
	FormatTable OutputFormat = "table"
	FormatJSON  OutputFormat = "json"
	FormatPlain OutputFormat = "plain"
)

// ParseOutputFormat validates a --format value.
func ParseOutputFormat(value string) (OutputFormat, error) {
	switch OutputFormat(value) {
	case FormatTable, FormatJSON, FormatPlain:
		return OutputFormat(value), nil
	default:
		return FormatTable, fmt.Errorf("--format %q: %w (allowed: table, json, plain)", value, errBadFormat)
	}
}

// printJSON renders v as indented JSON with a trailing newline.
func printJSON(cmd *cobra.Command, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}

	return printLine(cmd, "%s\n", encoded)
}

// printPlainRows renders key: value line blocks, one block per row and a
// blank line between rows; zero rows render nothing.
func printPlainRows(cmd *cobra.Command, keys []string, rows [][]string) error {
	for idx, row := range rows {
		if idx > 0 {
			if err := printLine(cmd, "\n"); err != nil {
				return err
			}
		}

		for col, key := range keys {
			if col >= len(row) {
				break
			}

			if err := printLine(cmd, "%s: %s\n", key, row[col]); err != nil {
				return err
			}
		}
	}

	return nil
}

// printPlainPairs renders a single key: value block.
func printPlainPairs(cmd *cobra.Command, pairs [][2]string) error {
	for _, pair := range pairs {
		if err := printLine(cmd, "%s: %s\n", pair[0], pair[1]); err != nil {
			return err
		}
	}

	return nil
}

// yesNo renders a boolean for key: value style output.
func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}
