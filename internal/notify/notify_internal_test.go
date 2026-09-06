package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestLog records what the test server received, mutex-guarded because
// the server serves each request on its own goroutine.
type requestLog struct {
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	method      string
	contentType string
	body        []byte
}

func (l *requestLog) record(r *http.Request) {
	body := make([]byte, r.ContentLength)
	_, _ = r.Body.Read(body)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.requests = append(l.requests, recordedRequest{
		method:      r.Method,
		contentType: r.Header.Get("Content-Type"),
		body:        body,
	})
}

func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.requests)
}

func TestPayloadGoldenJSON(t *testing.T) {
	t.Parallel()

	done := Payload{
		Event:        EventDone,
		RunID:        "run-20260907-120000-00ff0011",
		Account:      "default",
		Chats:        3,
		FilesMatched: 10,
		Downloaded:   7,
		Skipped:      1,
		Failed:       2,
		Interrupted:  0,
		Duplicates:   1,
		Bytes:        4096,
		TookSeconds:  12.5,
		Errors:       []string{"fetch 1/2/0: FLOOD_WAIT_60", "size mismatch"},
	}

	encoded, err := json.Marshal(done)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"event": "done",
		"run_id": "run-20260907-120000-00ff0011",
		"account": "default",
		"chats": 3,
		"files_matched": 10,
		"downloaded": 7,
		"skipped": 1,
		"failed": 2,
		"interrupted": 0,
		"duplicates": 1,
		"bytes": 4096,
		"took_seconds": 12.5,
		"errors": ["fetch 1/2/0: FLOOD_WAIT_60", "size mismatch"]
	}`, string(encoded), "done payload: exact shape and field order")

	parked := done
	parked.Event = EventParked
	parked.ParkedUntil = "2026-09-07T12:01:00Z"

	encoded, err = json.Marshal(parked)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"event": "parked",
		"run_id": "run-20260907-120000-00ff0011",
		"account": "default",
		"chats": 3,
		"files_matched": 10,
		"downloaded": 7,
		"skipped": 1,
		"failed": 2,
		"interrupted": 0,
		"duplicates": 1,
		"bytes": 4096,
		"took_seconds": 12.5,
		"parked_until": "2026-09-07T12:01:00Z",
		"errors": ["fetch 1/2/0: FLOOD_WAIT_60", "size mismatch"]
	}`, string(encoded), "parked payload: parked_until rides between took_seconds and errors")
}

func TestNotifyPostsJSON(t *testing.T) {
	t.Parallel()

	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{url: server.URL, http: server.Client()}

	err := client.Notify(t.Context(), Payload{Event: EventDone, RunID: "run-1", Errors: []string{}})
	require.NoError(t, err)

	require.Len(t, log.requests, 1)
	assert.Equal(t, http.MethodPost, log.requests[0].method)
	assert.Equal(t, "application/json", log.requests[0].contentType)

	var decoded Payload
	require.NoError(t, json.Unmarshal(log.requests[0].body, &decoded))
	assert.Equal(t, EventDone, decoded.Event)
	assert.Equal(t, "run-1", decoded.RunID)
}

func TestNotifyRetriesOnceThenSucceeds(t *testing.T) {
	t.Parallel()

	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)

		if log.count() == 1 {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{url: server.URL, http: &http.Client{Timeout: requestTimeout}}

	require.NoError(t, client.Notify(t.Context(), Payload{Event: EventDone}))
	assert.Equal(t, 2, log.count(), "one failed attempt plus exactly one retry")
}

func TestNotifyRetryExhaustedReturnsError(t *testing.T) {
	t.Parallel()

	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &Client{url: server.URL, http: &http.Client{Timeout: requestTimeout}}

	err := client.Notify(t.Context(), Payload{Event: EventDone})
	require.Error(t, err, "persistent failure surfaces to the caller")
	assert.Equal(t, 2, log.count(), "the attempt budget is the initial try plus one retry")
}

func TestNotifyTimeoutFailsFastAndRetries(t *testing.T) {
	t.Parallel()

	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{url: server.URL, http: &http.Client{Timeout: 20 * time.Millisecond}}

	started := time.Now()

	err := client.Notify(t.Context(), Payload{Event: EventDone})
	require.Error(t, err, "a hanging endpoint surfaces as an error")

	assert.Less(t, time.Since(started), 300*time.Millisecond, "the per-attempt timeout bounds delivery")
	assert.Equal(t, 2, log.count(), "the timeout consumed one retry too")
}
