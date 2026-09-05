package scan_test

import (
	"context"
	"testing"
	"time"

	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMinAgeFromOptions(t *testing.T) {
	t.Parallel()

	age, err := scan.MinAgeFromOptions(filters.Options{})
	require.NoError(t, err)
	assert.Zero(t, age)

	age, err = scan.MinAgeFromOptions(filters.Options{ChatMinAge: "1y"})
	require.NoError(t, err)
	assert.Equal(t, 365*24*time.Hour, age)

	age, err = scan.MinAgeFromOptions(filters.Options{ChatMinAge: "90d"})
	require.NoError(t, err)
	assert.Equal(t, 90*24*time.Hour, age)

	_, err = scan.MinAgeFromOptions(filters.Options{ChatMinAge: "soon"})
	require.Error(t, err)
	assert.ErrorIs(t, err, filters.ErrBadParse)
}

// ageProbeAPI answers the offset-bounded probe per user id: even ids have
// pre-cutoff history, odd ids do not.
type ageProbeAPI struct {
	fakeWalkAPI
	probes []*tg.MessagesGetHistoryRequest
}

//nolint:ireturn // mirrors the tg.Client method signature under test
func (f *ageProbeAPI) MessagesGetHistory(
	ctx context.Context, request *tg.MessagesGetHistoryRequest,
) (tg.MessagesMessagesClass, error) {
	f.probes = append(f.probes, request)

	user, _ := request.Peer.(*tg.InputPeerUser)

	if user != nil && user.UserID%2 == 0 {
		return &tg.MessagesMessagesSlice{
			Count:    1,
			Messages: []tg.MessageClass{&tg.Message{ID: 10}},
		}, nil
	}

	return &tg.MessagesMessages{}, nil
}

func TestFilterByMinAge(t *testing.T) {
	t.Parallel()

	api := &ageProbeAPI{}

	targets := []scan.Target{
		{InputPeer: &tg.InputPeerUser{UserID: 2}, Chat: filters.Chat{ID: 2}},
		{InputPeer: &tg.InputPeerUser{UserID: 3}, Chat: filters.Chat{ID: 3}},
		{InputPeer: &tg.InputPeerUser{UserID: 4}, Chat: filters.Chat{ID: 4}},
	}

	kept, err := scan.FilterByMinAge(context.Background(), api, targets, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, kept, 2)
	assert.Equal(t, int64(2), kept[0].Chat.ID)
	assert.Equal(t, int64(4), kept[1].Chat.ID)

	require.Len(t, api.probes, 3, "one probe per target")
	assert.Equal(t, 1, api.probes[0].Limit)
	assert.NotZero(t, api.probes[0].OffsetDate, "probe must bound by cutoff date")

	all, err := scan.FilterByMinAge(context.Background(), api, targets, 0)
	require.NoError(t, err)
	assert.Len(t, all, 3, "zero age is a no-op")

	probesBefore := len(api.probes)
	_, err = scan.FilterByMinAge(context.Background(), api, targets, 0)
	require.NoError(t, err)
	assert.Len(t, api.probes, probesBefore, "zero age skips probing")
}
