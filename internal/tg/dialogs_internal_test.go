package tg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestArchivedMatchesModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode     string
		archived bool
		want     bool
	}{
		{"only", true, true},
		{"only", false, false},
		{"exclude", true, false},
		{"exclude", false, true},
		{"any", true, true},
		{"any", false, true},
		{"", true, true},
		{"", false, true},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, archivedMatches(tc.mode, tc.archived),
			"mode %q archived %v", tc.mode, tc.archived)
	}
}
