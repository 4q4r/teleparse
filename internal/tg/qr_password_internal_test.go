package tg

import (
	"context"
	"errors"
	"testing"

	gotdtg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSessionPasswordNeeded(t *testing.T) {
	t.Parallel()

	require.True(t, isSessionPasswordNeeded(tgerr.New(401, "SESSION_PASSWORD_NEEDED")))
	require.True(t, isSessionPasswordNeeded(errors.Join(tgerr.New(401, "SESSION_PASSWORD_NEEDED"))))
	assert.False(t, isSessionPasswordNeeded(tgerr.New(401, "AUTH_KEY_UNREGISTERED")))
	assert.False(t, isSessionPasswordNeeded(assert.AnError))
}

func TestCompleteQRPassword(t *testing.T) {
	t.Parallel()

	t.Run("first try succeeds", func(t *testing.T) {
		t.Parallel()

		fake := &fakePasswordLogin{passwords: []string{"correct"}}
		authz, err := completeQRPassword(context.Background(), fake, scriptedAskQR([]string{"correct"}))
		require.NoError(t, err)
		require.NotNil(t, authz)
		assert.Equal(t, []string{"correct"}, fake.got)
	})

	t.Run("wrong then right", func(t *testing.T) {
		t.Parallel()

		fake := &fakePasswordLogin{
			passwords: []string{"correct"},
			failFirst: tgerr.New(400, "PASSWORD_HASH_INVALID"),
		}
		authz, err := completeQRPassword(context.Background(), fake, scriptedAskQR([]string{"wrong", "correct"}))
		require.NoError(t, err)
		require.NotNil(t, authz)
	})

	t.Run("three wrong exhaust", func(t *testing.T) {
		t.Parallel()

		fake := &fakePasswordLogin{failFirst: tgerr.New(400, "PASSWORD_HASH_INVALID")}
		_, err := completeQRPassword(context.Background(), fake, scriptedAskQR([]string{"a", "b", "c"}))
		require.Error(t, err)
		require.ErrorIs(t, err, ErrQRWrongPassword)
	})

	t.Run("nil ask surfaces hint error", func(t *testing.T) {
		t.Parallel()

		_, err := completeQRPassword(context.Background(), &fakePasswordLogin{}, nil)
		require.ErrorIs(t, err, ErrQRPasswordNoAsk)
	})

	t.Run("empty password re-asks without rpc", func(t *testing.T) {
		t.Parallel()

		fake := &fakePasswordLogin{passwords: []string{"x"}}
		_, err := completeQRPassword(context.Background(), fake, scriptedAskQR([]string{"", "x"}))
		require.NoError(t, err)
		assert.Len(t, fake.got, 1)
	})

	t.Run("unexpected rpc error propagates", func(t *testing.T) {
		t.Parallel()

		fake := &fakePasswordLogin{failFirst: tgerr.New(500, "INTERNAL")}
		_, err := completeQRPassword(context.Background(), fake, scriptedAskQR([]string{"x"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "complete 2FA")
	})
}

type fakePasswordLogin struct {
	passwords []string // accepted passwords; empty = never accepts
	failFirst error    // returned beyond passwords, or every call when passwords empty
	got       []string
}

func (f *fakePasswordLogin) Password(_ context.Context, password string) (*gotdtg.AuthAuthorization, error) {
	f.got = append(f.got, password)

	if len(f.passwords) > 0 && password == f.passwords[0] {
		return &gotdtg.AuthAuthorization{}, nil
	}

	if f.failFirst != nil {
		return nil, f.failFirst
	}

	return nil, tgerr.New(400, "PASSWORD_HASH_INVALID")
}

func scriptedAskQR(answers []string) func(string) (string, error) {
	calls := 0

	return func(string) (string, error) {
		defer func() { calls++ }()

		if calls >= len(answers) {
			return "", assert.AnError
		}

		return answers[calls], nil
	}
}
