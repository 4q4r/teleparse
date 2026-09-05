package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStyleOptionsEnabledMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts StyleOptions
		want bool
	}{
		{"tty plain", StyleOptions{IsTTY: true}, true},
		{"flag off", StyleOptions{IsTTY: true, NoColorFlag: true}, false},
		{"no-ascii off", StyleOptions{IsTTY: true, NoASCIIFlag: true}, false},
		{"env off", StyleOptions{IsTTY: true, NoColorEnv: true}, false},
		{"non-tty off", StyleOptions{IsTTY: false}, false},
		{"everything off", StyleOptions{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.opts.Enabled())
		})
	}
}

func TestStylerDisabledIsPassthrough(t *testing.T) {
	t.Parallel()

	styler := NewStyler(false)

	assert.False(t, styler.Enabled())
	assert.Equal(t, "error:", styler.ErrorPrefix())
	assert.Equal(t, "+", styler.Success("+"))
	assert.Equal(t, "FAIL", styler.Failure("FAIL"))
	assert.Equal(t, "warn", styler.Warning("warn"))
	assert.Equal(t, "hint", styler.Dim("hint"))
}

func TestStylerEnabledEmitsANSI(t *testing.T) {
	t.Parallel()

	styler := NewStyler(true)

	assert.True(t, styler.Enabled())

	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"error prefix", styler.ErrorPrefix(), "error:"},
		{"success", styler.Success("+"), "+"},
		{"failure", styler.Failure("FAIL"), "FAIL"},
		{"warning", styler.Warning("fix:"), "fix:"},
		{"dim", styler.Dim("Choice [1]"), "Choice [1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.NotEqual(t, tc.want, tc.got, "enabled styler must style the text")
			assert.Contains(t, tc.got, tc.want, "styled text must keep the plain content")
			assert.Contains(t, tc.got, "\x1b[", "styled text must carry ANSI escapes")
			assert.False(t, containsNonASCII(tc.got), "visible text must stay ASCII")
		})
	}
}

func TestDoctorBadgeColoredWhenEnabled(t *testing.T) {
	t.Parallel()

	plain := NewStyler(false)
	assert.Equal(t, "PASS", doctorBadge(plain, "PASS"))
	assert.Equal(t, "FAIL", doctorBadge(plain, "FAIL"))
	assert.Equal(t, "SKIP", doctorBadge(plain, "SKIP"))

	color := NewStyler(true)
	assert.Contains(t, doctorBadge(color, "PASS"), "\x1b[")
	assert.Contains(t, doctorBadge(color, "FAIL"), "\x1b[")
	assert.Contains(t, doctorBadge(color, "SKIP"), "\x1b[")
}

// containsNonASCII reports whether s holds any rune above 0x7f.
func containsNonASCII(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r > 0x7f }) >= 0
}
