package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLoginMethodExplicitFlagsSkipMenu(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		qr       bool
		phone    string
		telethon string
		tdata    string
		want     loginPlan
	}{
		{name: "qr flag", qr: true, want: loginPlan{QR: true}},
		{name: "phone flag", phone: "+15551234567", want: loginPlan{Phone: "+15551234567"}},
		{name: "telethon flag", telethon: "/tmp/s.sqlite", want: loginPlan{TelethonPath: "/tmp/s.sqlite"}},
		{name: "tdata flag", tdata: "/tmp/tdata", want: loginPlan{TDataDir: "/tmp/tdata"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan, err := resolveLoginMethod(tc.qr, tc.phone, tc.telethon, tc.tdata, true, failingAsk)
			require.NoError(t, err)
			assert.Equal(t, tc.want, plan)
		})
	}
}

func TestResolveLoginMethodNonInteractiveDefaultsToPhone(t *testing.T) {
	t.Parallel()

	plan, err := resolveLoginMethod(false, "", "", "", false, failingAsk)
	require.NoError(t, err)
	assert.Equal(t, loginPlan{}, plan)
}

func TestResolveLoginMethodMenuChoices(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		answers []string
		want    loginPlan
	}{
		{name: "default on empty", answers: []string{""}, want: loginPlan{}},
		{name: "explicit phone", answers: []string{"1"}, want: loginPlan{}},
		{name: "qr", answers: []string{"2"}, want: loginPlan{QR: true}},
		{name: "telethon with path", answers: []string{"3", "/old.session"}, want: loginPlan{TelethonPath: "/old.session"}},
		{name: "tdata with path", answers: []string{"4", "/tdata"}, want: loginPlan{TDataDir: "/tdata"}},
		{name: "choice with whitespace", answers: []string{" 2 "}, want: loginPlan{QR: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ask := scriptedAsk(tc.answers)
			plan, err := resolveLoginMethod(false, "", "", "", true, ask)
			require.NoError(t, err)
			assert.Equal(t, tc.want, plan)
		})
	}
}

func TestResolveLoginMethodMenuErrors(t *testing.T) {
	t.Parallel()

	_, err := resolveLoginMethod(false, "", "", "", true, scriptedAsk([]string{"7"}))
	require.Error(t, err)
	require.ErrorIs(t, err, errBadLoginChoice)

	_, err = resolveLoginMethod(false, "", "", "", true, scriptedAsk([]string{"3", ""}))
	require.Error(t, err)
	require.ErrorIs(t, err, errLoginPathRequired)

	_, err = resolveLoginMethod(false, "", "", "", true, scriptedAsk([]string{"4", ""}))
	require.Error(t, err)
	require.ErrorIs(t, err, errLoginPathRequired)

	_, err = resolveLoginMethod(false, "", "", "", true, failingAsk)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read login method")
}

// scriptedAsk returns the next canned answer on every call.
func scriptedAsk(answers []string) func(string) (string, error) {
	calls := 0

	return func(string) (string, error) {
		defer func() { calls++ }()

		if calls >= len(answers) {
			return "", assert.AnError
		}

		return answers[calls], nil
	}
}

// failingAsk fails every call, proving the menu is never reached.
func failingAsk(string) (string, error) {
	return "", assert.AnError
}
