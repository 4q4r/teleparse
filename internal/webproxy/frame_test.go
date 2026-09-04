package webproxy_test

import (
	"encoding/binary"
	"encoding/hex"
	"teleparse/internal/webproxy"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFrameEncodeGoldenVectors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		frame webproxy.Frame
		want  string
	}{
		{
			name:  "OPEN stream 1",
			frame: webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1},
			want:  "0100000100000000",
		},
		{
			name:  "DATA stream 0x010203 payload hi",
			frame: webproxy.Frame{Type: webproxy.TypeData, StreamID: 0x010203, Payload: []byte("hi")},
			want:  "02010203000000026869",
		},
		{
			name:  "CLOSE stream max",
			frame: webproxy.Frame{Type: webproxy.TypeClose, StreamID: 0xFFFFFF},
			want:  "03ffffff00000000",
		},
		{
			name:  "WINDOW stream 5 delta 1",
			frame: webproxy.Frame{Type: webproxy.TypeWindow, StreamID: 5, Payload: []byte{0, 0, 0, 1}},
			want:  "040000050000000400000001",
		},
		{
			name:  "PING echo token t",
			frame: webproxy.Frame{Type: webproxy.TypePing, Payload: []byte("t")},
			want:  "050000000000000174",
		},
		{
			name:  "PONG echo token t",
			frame: webproxy.Frame{Type: webproxy.TypePong, Payload: []byte("t")},
			want:  "060000000000000174",
		},
		{
			name:  "HELLO payload byte 01",
			frame: webproxy.Frame{Type: webproxy.TypeHello, Payload: []byte{0x01}},
			want:  "100000000000000101",
		},
		{
			name:  "WELCOME empty",
			frame: webproxy.Frame{Type: webproxy.TypeWelcome},
			want:  "1100000000000000",
		},
		{
			name:  "BYE empty reason",
			frame: webproxy.Frame{Type: webproxy.TypeBye},
			want:  "1f00000000000000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want, err := hex.DecodeString(tc.want)
			require.NoError(t, err)
			require.Equal(t, want, webproxy.Encode(tc.frame))
		})
	}
}

func TestFrameDecodeGoldenVectors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  webproxy.Frame
	}{
		{
			name:  "OPEN stream 1",
			input: "0100000100000000",
			want:  webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1},
		},
		{
			name:  "DATA stream 0x010203 payload hi",
			input: "02010203000000026869",
			want:  webproxy.Frame{Type: webproxy.TypeData, StreamID: 0x010203, Payload: []byte("hi")},
		},
		{
			name:  "WINDOW stream 5 delta 1",
			input: "040000050000000400000001",
			want:  webproxy.Frame{Type: webproxy.TypeWindow, StreamID: 5, Payload: []byte{0, 0, 0, 1}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input, err := hex.DecodeString(tc.input)
			require.NoError(t, err)

			got, err := webproxy.Decode(input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestFrameDecodeRejectsMalformed(t *testing.T) {
	t.Parallel()

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.Decode(nil)
		require.ErrorIs(t, err, webproxy.ErrIncompleteFrame)
	})

	t.Run("truncated header", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.Decode([]byte{0x01, 0x00, 0x00})
		require.ErrorIs(t, err, webproxy.ErrIncompleteFrame)
	})

	t.Run("payload shorter than length", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.Decode([]byte{0x02, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02, 0x68})
		require.ErrorIs(t, err, webproxy.ErrIncompleteFrame)
	})

	t.Run("trailing bytes after one frame", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.Decode([]byte{
			0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
			0x11,
		})
		require.ErrorIs(t, err, webproxy.ErrTrailingBytes)
	})

	t.Run("payload exceeds max", func(t *testing.T) {
		t.Parallel()

		oversize := make([]byte, webproxy.HeaderSize+webproxy.MaxPayload-1)
		oversize[0] = byte(webproxy.TypeData)
		oversize[3] = 0x01
		binary.BigEndian.PutUint32(oversize[4:8], webproxy.MaxPayload)

		_, err := webproxy.Decode(oversize)
		require.ErrorIs(t, err, webproxy.ErrIncompleteFrame)

		oversize = make([]byte, webproxy.HeaderSize+webproxy.MaxPayload+1)
		oversize[0] = byte(webproxy.TypeData)
		oversize[3] = 0x01
		binary.BigEndian.PutUint32(oversize[4:8], webproxy.MaxPayload+1)

		_, err = webproxy.Decode(oversize)
		require.ErrorIs(t, err, webproxy.ErrPayloadTooLarge)
	})
}

func TestFrameDecodeAll(t *testing.T) {
	t.Parallel()

	t.Run("two frames", func(t *testing.T) {
		t.Parallel()

		input, err := hex.DecodeString("0100000100000000" + "02010203000000026869")
		require.NoError(t, err)

		frames, err := webproxy.DecodeAll(input)
		require.NoError(t, err)
		require.Len(t, frames, 2)
		require.Equal(t, webproxy.TypeOpen, frames[0].Type)
		require.Equal(t, webproxy.TypeData, frames[1].Type)
		require.Equal(t, "hi", string(frames[1].Payload))
	})

	t.Run("empty batch", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.DecodeAll(nil)
		require.ErrorIs(t, err, webproxy.ErrEmptyBatch)
	})

	t.Run("too many frames", func(t *testing.T) {
		t.Parallel()

		input := make([]byte, 0, (webproxy.MaxBatchFrames+1)*webproxy.HeaderSize)
		for range webproxy.MaxBatchFrames + 1 {
			input = append(input, 0x03, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00)
		}

		_, err := webproxy.DecodeAll(input)
		require.ErrorIs(t, err, webproxy.ErrTooManyFrames)
	})

	t.Run("incomplete tail", func(t *testing.T) {
		t.Parallel()

		input, err := hex.DecodeString("0100000100000000" + "02")
		require.NoError(t, err)

		_, err = webproxy.DecodeAll(input)
		require.ErrorIs(t, err, webproxy.ErrIncompleteFrame)
	})
}

func TestFrameWindowHelpers(t *testing.T) {
	t.Parallel()

	t.Run("roundtrip", func(t *testing.T) {
		t.Parallel()

		payload := webproxy.WindowPayload(0x01020304)
		require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, payload)

		amount, err := webproxy.WindowAmount(payload)
		require.NoError(t, err)
		require.Equal(t, uint32(0x01020304), amount)
	})

	t.Run("wrong size", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.WindowAmount([]byte{1, 2, 3})
		require.ErrorIs(t, err, webproxy.ErrWindowAmount)
	})

	t.Run("zero delta", func(t *testing.T) {
		t.Parallel()

		_, err := webproxy.WindowAmount([]byte{0, 0, 0, 0})
		require.ErrorIs(t, err, webproxy.ErrWindowAmount)
	})
}

func TestFrameValidateClientShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		frame   webproxy.Frame
		wantErr error
	}{
		{
			name:  "OPEN valid",
			frame: webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1},
		},
		{
			name:    "OPEN with payload",
			frame:   webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1, Payload: []byte("x")},
			wantErr: webproxy.ErrInvalidShape,
		},
		{
			name:  "DATA valid",
			frame: webproxy.Frame{Type: webproxy.TypeData, StreamID: 1, Payload: []byte("x")},
		},
		{
			name:    "DATA empty payload",
			frame:   webproxy.Frame{Type: webproxy.TypeData, StreamID: 1},
			wantErr: webproxy.ErrInvalidShape,
		},
		{
			name:  "WINDOW valid",
			frame: webproxy.Frame{Type: webproxy.TypeWindow, StreamID: 1, Payload: webproxy.WindowPayload(7)},
		},
		{
			name:  "PONG on stream zero",
			frame: webproxy.Frame{Type: webproxy.TypePong, Payload: []byte("token")},
		},
		{
			name:    "DATA on stream zero",
			frame:   webproxy.Frame{Type: webproxy.TypeData, Payload: []byte("x")},
			wantErr: webproxy.ErrInvalidShape,
		},
		{
			name:    "HELLO from client after bootstrap",
			frame:   webproxy.Frame{Type: webproxy.TypeHello},
			wantErr: webproxy.ErrInvalidShape,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := webproxy.ValidateClient(tc.frame)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestFrameValidateServerShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		frame   webproxy.Frame
		wantErr error
	}{
		{
			name:  "DATA valid",
			frame: webproxy.Frame{Type: webproxy.TypeData, StreamID: 1, Payload: []byte("x")},
		},
		{
			name:  "WINDOW valid",
			frame: webproxy.Frame{Type: webproxy.TypeWindow, StreamID: 1, Payload: webproxy.WindowPayload(7)},
		},
		{
			name:  "CLOSE valid",
			frame: webproxy.Frame{Type: webproxy.TypeClose, StreamID: 1},
		},
		{
			name:  "PING on stream zero",
			frame: webproxy.Frame{Type: webproxy.TypePing, Payload: []byte("t")},
		},
		{
			name:  "BYE on stream zero",
			frame: webproxy.Frame{Type: webproxy.TypeBye},
		},
		{
			name:    "OPEN from relay",
			frame:   webproxy.Frame{Type: webproxy.TypeOpen, StreamID: 1},
			wantErr: webproxy.ErrInvalidShape,
		},
		{
			name:    "PONG from relay",
			frame:   webproxy.Frame{Type: webproxy.TypePong, Payload: []byte("t")},
			wantErr: webproxy.ErrInvalidShape,
		},
		{
			name:    "DATA empty payload",
			frame:   webproxy.Frame{Type: webproxy.TypeData, StreamID: 1},
			wantErr: webproxy.ErrInvalidShape,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := webproxy.ValidateServer(tc.frame)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestFrameUnknownTypeError(t *testing.T) {
	t.Parallel()

	_, err := webproxy.Decode([]byte{0x99, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00})
	require.ErrorIs(t, err, webproxy.ErrUnknownType)
	require.NotErrorIs(t, err, webproxy.ErrTrailingBytes)
}
