package tlmock_test

import (
	"teleparse/internal/testutil/tlmock"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	tg "github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:ireturn // handler fixtures must return bin.Object to feed the invoker seam
func historyHandler(_ *tg.MessagesGetHistoryRequest) (bin.Object, error) {
	return tlmock.HistoryPage([]tg.MessageClass{
		tlmock.Msg(1, "hello", tlmock.WithFrom(10)),
	}, []tg.UserClass{tlmock.User(10, "Alice")}, nil), nil
}

func TestInvokerRoundTripsRequestAndResponse(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()
	tlmock.HandleFunc(mock, historyHandler)

	result, err := mock.Client().MessagesGetHistory(t.Context(), &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
	})
	require.NoError(t, err)

	page, ok := result.(*tg.MessagesMessages)
	require.True(t, ok)
	require.Len(t, page.Messages, 1)

	msg, ok := page.Messages[0].(*tg.Message)
	require.True(t, ok)
	assert.Equal(t, 1, msg.ID)
	assert.Equal(t, "hello", msg.Message)

	from, ok := msg.GetFromID()
	require.True(t, ok)

	user, ok := from.(*tg.PeerUser)
	require.True(t, ok)
	assert.Equal(t, int64(10), user.UserID)
}

func TestInvokerRecordsDecodedRequestCopies(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()
	tlmock.HandleFunc(mock, historyHandler)

	_, err := mock.Client().MessagesGetHistory(t.Context(), &tg.MessagesGetHistoryRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
		MinID:    5,
		MaxID:    95,
		OffsetID: 77,
	})
	require.NoError(t, err)

	recorded := tlmock.Requests[tg.MessagesGetHistoryRequest](mock)
	require.Len(t, recorded, 1)

	peer, ok := recorded[0].Peer.(*tg.InputPeerChannel)
	require.True(t, ok)
	assert.Equal(t, int64(30), peer.ChannelID)
	assert.Equal(t, int64(300), peer.AccessHash)
	assert.Equal(t, 5, recorded[0].MinID)
	assert.Equal(t, 95, recorded[0].MaxID)
	assert.Equal(t, 77, recorded[0].OffsetID)

	assert.Equal(t, "*tg.MessagesGetHistoryRequest", mock.Recorded()[0].Type)
}

func TestInvokerRejectsUnhandledRequestType(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()

	_, err := mock.Client().MessagesGetHistory(t.Context(), &tg.MessagesGetHistoryRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tg.MessagesGetHistoryRequest")
}

func TestInvokerPropagatesRPCErrors(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()
	tlmock.HandleFunc(mock,
		func(*tg.MessagesGetHistoryRequest) (bin.Object, error) {
			return nil, tlmock.FloodWait(30)
		})

	_, err := mock.Client().MessagesGetHistory(t.Context(), &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerChannel{ChannelID: 30, AccessHash: 300},
	})
	require.Error(t, err)

	wait, ok := tgerr.AsFloodWait(err)
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, wait)
}

func TestInvokerHandlesVectorResults(t *testing.T) {
	t.Parallel()

	mock := tlmock.New()
	tlmock.HandleFunc(mock,
		func(request *tg.UsersGetUsersRequest) (bin.Object, error) {
			require.Len(t, request.ID, 1)

			return &tg.UserClassVector{Elems: []tg.UserClass{tlmock.User(11, "Bob")}}, nil
		})

	users, err := mock.Client().UsersGetUsers(t.Context(),
		[]tg.InputUserClass{&tg.InputUser{UserID: 11}})
	require.NoError(t, err)
	require.Len(t, users, 1)

	user, ok := users[0].(*tg.User)
	require.True(t, ok)
	assert.Equal(t, "Bob", user.FirstName)
}
