package tg

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	gotdtg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeQRClient drives the qrLoginer seam: it shows scripted tokens in order,
// then returns the scripted outcome (authorization, error, or deadline wait).
type fakeQRClient struct {
	tokens  [][]byte
	authz   *gotdtg.AuthAuthorization
	retErr  error
	block   bool
	shown   [][]byte
	showErr error
}

func (f *fakeQRClient) Auth(
	ctx context.Context,
	_ qrlogin.LoggedIn,
	show func(context.Context, qrlogin.Token) error,
	_ ...int64,
) (*gotdtg.AuthAuthorization, error) {
	for _, raw := range f.tokens {
		f.shown = append(f.shown, raw)

		if f.showErr != nil {
			return nil, f.showErr
		}

		if err := show(ctx, qrlogin.NewToken(raw, 0)); err != nil {
			return nil, err
		}
	}

	if f.block {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	return f.authz, f.retErr
}

func qrDeadlineCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(t.Context(), time.Second)
}

func TestRunQRLoginFirstTokenRendered(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	fake := &fakeQRClient{
		tokens: [][]byte{[]byte("first-token")},
		authz:  &gotdtg.AuthAuthorization{},
	}

	ctx, cancel := qrDeadlineCtx(t)
	defer cancel()

	authz, err := runQRLogin(ctx, fake, nil, &out, true)
	require.NoError(t, err)
	assert.Same(t, fake.authz, authz)
	assert.Contains(t, out.String(), "tg://login?token="+base64.URLEncoding.EncodeToString([]byte("first-token")))
	assert.Contains(t, out.String(), "Settings -> Devices -> Link Desktop Device")
}

func TestRunQRLoginRotationRendersWholeBlockAgain(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	fake := &fakeQRClient{tokens: [][]byte{[]byte("one"), []byte("two")}, authz: &gotdtg.AuthAuthorization{}}

	ctx, cancel := qrDeadlineCtx(t)
	defer cancel()

	_, err := runQRLogin(ctx, fake, nil, &out, true)
	require.NoError(t, err)

	first := base64.URLEncoding.EncodeToString([]byte("one"))
	second := base64.URLEncoding.EncodeToString([]byte("two"))

	assert.Contains(t, out.String(), "tg://login?token="+first)
	assert.Contains(t, out.String(), "tg://login?token="+second)
	assert.Equal(t, 1, strings.Count(out.String(), "token refreshed"), "rotation note must appear exactly once for two shows")
}

func TestRunQRLoginTimeout(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	fake := &fakeQRClient{tokens: [][]byte{[]byte("never-approved")}, block: true}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := runQRLogin(ctx, fake, nil, &out, true)
	require.ErrorIs(t, err, ErrQRTimeout)
}

func TestRunQRLoginForeignErrorPassesThrough(t *testing.T) {
	t.Parallel()

	boom := errors.New("flood wait")

	fake := &fakeQRClient{tokens: [][]byte{[]byte("x")}, retErr: boom}

	ctx, cancel := qrDeadlineCtx(t)
	defer cancel()

	_, err := runQRLogin(ctx, fake, nil, io.Discard, true)
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrQRTimeout)
}

func TestRenderQRASCIIOnlyMode(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, renderQR(&out, qrlogin.NewToken([]byte{1, 2, 250}, 0), false))

	text := out.String()

	for _, symbol := range text {
		assert.LessOrEqual(t, symbol, rune(127), "--no-ascii and non-TTY output must stay pure ASCII: %q", symbol)
	}

	assert.Contains(t, text, "tg://login?token="+base64.URLEncoding.EncodeToString([]byte{1, 2, 250}))
	assert.NotContains(t, text, "scan the QR", "no QR block instructions without a block")
}

func TestRenderQRHalfBlockMatrix(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, renderQR(&out, qrlogin.NewToken([]byte("full-matrix-token"), 0), true))

	text := out.String()
	assert.Contains(t, text, "tg://login?token="+base64.URLEncoding.EncodeToString([]byte("full-matrix-token")))

	blocks := strings.Count(text, "\u2588") + strings.Count(text, "\u2580") + strings.Count(text, "\u2584")
	assert.Positive(t, blocks, "full mode must draw the half-block matrix")
}

func TestRenderQRTGURLIsBase64URL(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	raw := []byte{0, 255, 250, 1}

	require.NoError(t, renderQR(&out, qrlogin.NewToken(raw, 0), false))
	assert.Contains(t, out.String(), "tg://login?token="+base64.URLEncoding.EncodeToString(raw))
}

func TestRunQRLoginShowFailurePropagates(t *testing.T) {
	t.Parallel()

	fake := &fakeQRClient{tokens: [][]byte{[]byte("x")}, showErr: errors.New("stderr closed")}

	ctx, cancel := qrDeadlineCtx(t)
	defer cancel()

	_, err := runQRLogin(ctx, fake, nil, errWriter{}, true)
	require.ErrorContains(t, err, "stderr closed")
}

// errWriter fails every write so renderQR surfaces IO errors.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("stderr closed")
}

// Compile-time proof that gotd's qrlogin.QR satisfies the testable seam.
var _ qrLoginer = qrlogin.QR{}

// fakeFlowClient implements auth.FlowClient: sign-in always reports the 2FA
// branch, so the flow must prompt for the password and submit it.
type fakeFlowClient struct {
	sentCode    *gotdtg.AuthSentCode
	passwordGot string
	calls       []string
}

//nolint:ireturn // gotd's FlowClient contract returns the interface
func (f *fakeFlowClient) SendCode(
	_ context.Context, _ string, _ auth.SendCodeOptions,
) (gotdtg.AuthSentCodeClass, error) {
	f.calls = append(f.calls, "send_code")

	return f.sentCode, nil
}

func (f *fakeFlowClient) SignIn(_ context.Context, _, _, _ string) (*gotdtg.AuthAuthorization, error) {
	f.calls = append(f.calls, "sign_in")

	return nil, auth.ErrPasswordAuthNeeded
}

func (f *fakeFlowClient) Password(_ context.Context, password string) (*gotdtg.AuthAuthorization, error) {
	f.calls = append(f.calls, "password")
	f.passwordGot = password

	return &gotdtg.AuthAuthorization{}, nil
}

func (f *fakeFlowClient) SignUp(_ context.Context, _ auth.SignUp) (*gotdtg.AuthAuthorization, error) {
	return nil, errors.New("unexpected sign-up")
}

func TestFlowWiresTwoFAPasswordAfterCode(t *testing.T) {
	t.Parallel()

	ask := &scriptPrompter{lines: []string{"54321"}, hidden: []string{"s3cret"}}
	flow := auth.NewFlow(promptAuth{phone: "+15551234567", ask: ask}, auth.SendCodeOptions{})

	client := &fakeFlowClient{
		sentCode: &gotdtg.AuthSentCode{PhoneCodeHash: "hash"},
	}

	require.NoError(t, flow.Run(t.Context(), client))

	assert.Equal(t, "s3cret", client.passwordGot, "the prompted 2FA password must reach the SRP sign-in call")
	assert.Equal(t, []string{"send_code", "sign_in", "password"}, client.calls)
	require.Len(t, ask.asked, 1, "code asked exactly once before the password")
	require.Len(t, ask.askedHid, 1, "password asked exactly once, hidden")
}
