package scan_test

import (
	"errors"
	"testing"

	"github.com/4q4r/teleparse/internal/scan"

	tg "github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldWalkIncrementally(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		watermark int64
		newest    int64
		want      bool
	}{
		{name: "zero watermark always walks full", watermark: 0, newest: 500, want: false},
		{name: "newest reached watermark", watermark: 500, newest: 500, want: true},
		{name: "newest passed watermark", watermark: 500, newest: 900, want: true},
		{name: "newest below watermark is cleared", watermark: 500, newest: 12, want: false},
		{name: "empty chat with watermark reads as cleared", watermark: 500, newest: 0, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, scan.ShouldWalkIncrementally(tc.watermark, tc.newest))
		})
	}
}

func TestHistoryCleared(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		watermark int64
		newest    int64
		want      bool
	}{
		{name: "newest strictly below watermark", watermark: 500, newest: 499, want: true},
		{name: "newest far below watermark", watermark: 90000, newest: 5, want: true},
		{name: "newest equal to watermark is not cleared", watermark: 500, newest: 500, want: false},
		{name: "newest above watermark is not cleared", watermark: 500, newest: 501, want: false},
		{name: "zero watermark is never cleared", watermark: 0, newest: 0, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, scan.HistoryCleared(tc.watermark, tc.newest))
		})
	}
}

func TestNewestMessageID(t *testing.T) {
	t.Parallel()

	peer := &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300}

	t.Run("returns first history message id", func(t *testing.T) {
		t.Parallel()

		api := &fakeWalkAPI{history: messagesResult([]tg.MessageClass{
			textMessage(812, 0, "newest"), textMessage(700, 0, "older"),
		})}

		id, err := scan.NewestMessageID(t.Context(), api, peer)
		require.NoError(t, err)
		assert.Equal(t, int64(812), id)
		require.NotNil(t, api.historyReq)
		assert.Equal(t, 1, api.historyReq.Limit, "probe must fetch a single message")
	})

	t.Run("empty chat reports zero", func(t *testing.T) {
		t.Parallel()

		id, err := scan.NewestMessageID(t.Context(), &fakeWalkAPI{}, peer)
		require.NoError(t, err)
		assert.Zero(t, id)
	})

	t.Run("slice result class is readable", func(t *testing.T) {
		t.Parallel()

		api := &fakeWalkAPI{history: &tg.MessagesMessagesSlice{
			Messages: []tg.MessageClass{textMessage(44, 0, "x")}, Count: 1,
		}}

		id, err := scan.NewestMessageID(t.Context(), api, peer)
		require.NoError(t, err)
		assert.Equal(t, int64(44), id)
	})

	t.Run("not modified reports zero", func(t *testing.T) {
		t.Parallel()

		api := &fakeWalkAPI{history: &tg.MessagesMessagesNotModified{}}

		id, err := scan.NewestMessageID(t.Context(), api, peer)
		require.NoError(t, err)
		assert.Zero(t, id)
	})

	t.Run("transport error propagates", func(t *testing.T) {
		t.Parallel()

		api := &fakeWalkAPI{historyErr: errors.New("RPC_CALL_FAIL")}

		_, err := scan.NewestMessageID(t.Context(), api, peer)
		require.Error(t, err)
		assert.ErrorContains(t, err, "RPC_CALL_FAIL")
	})
}
