package download_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/download"

	"github.com/stretchr/testify/assert"
)

// TestTopFailures pins the top-N FAIL-reason ranking: most frequent first,
// ties broken lexically, never nil, truncated to the limit.
func TestTopFailures(t *testing.T) {
	t.Parallel()

	res := download.Result{FailReasons: map[string]int64{
		"flood wait 60s":  5,
		"size mismatch":   5,
		"message gone":    2,
		"takeout too big": 1,
	}}

	assert.Equal(t,
		[]string{"flood wait 60s", "size mismatch", "message gone"},
		res.TopFailures(3), "frequency desc, lexical tie-break, truncated")

	assert.Equal(t,
		[]string{"flood wait 60s", "size mismatch", "message gone", "takeout too big"},
		res.TopFailures(10), "limit beyond the reason count returns all")

	empty := download.Result{}
	assert.NotNil(t, empty.TopFailures(3), "no failures yield a non-nil empty slice")
	assert.Empty(t, empty.TopFailures(3))
}
