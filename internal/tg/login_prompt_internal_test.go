package tg

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
	gotdtg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptPrompter answers scripted lines, recording every prompt it saw.
type scriptPrompter struct {
	lines    []string
	hidden   []string
	asked    []string
	askedHid []string
	failAt   int
}

func (p *scriptPrompter) Line(prompt string) (string, error) {
	p.asked = append(p.asked, prompt)

	if p.failAt == len(p.asked) {
		return "", errors.New("terminal closed")
	}

	answer := p.lines[0]
	p.lines = p.lines[1:]

	return answer, nil
}

func (p *scriptPrompter) Hidden(prompt string) (string, error) {
	p.askedHid = append(p.askedHid, prompt)

	if p.failAt == len(p.askedHid) {
		return "", errors.New("terminal closed")
	}

	answer := p.hidden[0]
	p.hidden = p.hidden[1:]

	return answer, nil
}

func TestPromptAuthPhonePreSupplied(t *testing.T) {
	t.Parallel()

	flow := promptAuth{phone: "+15551234567", ask: &scriptPrompter{}}

	phone, err := flow.Phone(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "+15551234567", phone)
}

func TestPromptAuthPhonePreSuppliedInvalid(t *testing.T) {
	t.Parallel()

	flow := promptAuth{phone: "5551234567", ask: &scriptPrompter{}}

	_, err := flow.Phone(t.Context())
	require.ErrorIs(t, err, ErrBadPhone)
}

func TestPromptAuthPhonePromptsUntilValid(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{lines: []string{"  5551234567 ", "+15551234567"}}
	flow := promptAuth{ask: ask}

	phone, err := flow.Phone(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "+15551234567", phone)
	require.Len(t, ask.asked, 2, "an invalid answer must be re-asked exactly once")
}

func TestPromptAuthPhonePrompterFailure(t *testing.T) {
	t.Parallel()

	flow := promptAuth{ask: &scriptPrompter{failAt: 1}}

	_, err := flow.Phone(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read phone")
}

func TestPromptAuthPhoneContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	flow := promptAuth{ask: &scriptPrompter{lines: []string{"nope"}}}

	_, err := flow.Phone(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "phone entry")
	require.ErrorIs(t, err, context.Canceled)
}

func TestPromptAuthCodeAndPassword(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{lines: []string{"  12345  "}, hidden: []string{"hunter2"}}
	flow := promptAuth{ask: ask}

	code, err := flow.Code(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, "12345", code, "codes are trimmed before use")

	password, err := flow.Password(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "hunter2", password, "passwords are not trimmed")
	require.Len(t, ask.askedHid, 1)
}

func TestPromptAuthCodePrompterFailure(t *testing.T) {
	t.Parallel()

	flow := promptAuth{ask: &scriptPrompter{failAt: 1}}

	_, err := flow.Code(t.Context(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read code")
}

func TestPromptAuthPasswordPrompterFailure(t *testing.T) {
	t.Parallel()

	flow := promptAuth{ask: &scriptPrompter{failAt: 1}}

	_, err := flow.Password(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read password")
}

func TestPromptAuthTermsAndSignUp(t *testing.T) {
	t.Parallel()

	flow := promptAuth{ask: &scriptPrompter{}}

	require.NoError(t, flow.AcceptTermsOfService(t.Context(), gotdtg.HelpTermsOfService{}))

	_, err := flow.SignUp(t.Context())
	require.ErrorIs(t, err, ErrSignUpUnsupported)
}

// Compile-time interface proof that promptAuth satisfies gotd's flow seam.
var _ auth.UserAuthenticator = promptAuth{}
