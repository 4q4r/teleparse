// Package webproxy implements the Telegram WEB-proxy v1 client (tproxy
// protocol) as a gotd dcs.Resolver, multiplexing MTProto streams over a
// bootstrap-authenticated carrier session.
package webproxy

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Type is a shared-frame type identifier from the WEB-proxy v1 protocol.
type Type byte

// Frame types as defined by PROTOCOL.md of telegramdesktop/tproxy-server.
const (
	TypeOpen    Type = 0x01
	TypeData    Type = 0x02
	TypeClose   Type = 0x03
	TypeWindow  Type = 0x04
	TypePing    Type = 0x05
	TypePong    Type = 0x06
	TypeHello   Type = 0x10
	TypeWelcome Type = 0x11
	TypeBye     Type = 0x1F
)

// Wire limits from PROTOCOL.md.
const (
	// HeaderSize is the fixed shared-frame header size in bytes.
	HeaderSize = 8
	// MaxPayload is the maximum frame payload size.
	MaxPayload = 1024 * 1024
	// MaxStreamID is the highest encodable u24 stream id.
	MaxStreamID = 0xFFFFFF
	// MaxBatchFrames is the maximum number of frames in one carrier body.
	MaxBatchFrames = 4096
	// MaxPongPayload is the maximum PING echo token size.
	MaxPongPayload = 64
)

// Frame decode errors.
var (
	ErrIncompleteFrame = errors.New("incomplete frame")
	ErrTrailingBytes   = errors.New("trailing bytes after frame")
	ErrPayloadTooLarge = errors.New("frame payload exceeds limit")
	ErrEmptyBatch      = errors.New("empty frame batch")
	ErrTooManyFrames   = errors.New("frame batch contains too many frames")
	ErrUnknownType     = errors.New("unknown frame type")
	ErrWindowAmount    = errors.New("invalid WINDOW payload")
	ErrInvalidShape    = errors.New("frame shape is invalid for direction")
)

// Wire layout constants for header field encoding.
const (
	typeFieldShift       = 24
	u24HiShift           = 16
	u24LoShift           = 8
	windowPayloadSize    = 4
	typeStreamFieldSize  = 4
	initialBatchCapacity = 4
)

// Frame is one decoded shared frame: header plus payload slice.
type Frame struct {
	Type     Type
	StreamID uint32
	Payload  []byte
}

// String returns the protocol name of the frame type.
func (t Type) String() string {
	switch t {
	case TypeOpen:
		return "OPEN"
	case TypeData:
		return "DATA"
	case TypeClose:
		return "CLOSE"
	case TypeWindow:
		return "WINDOW"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	case TypeHello:
		return "HELLO"
	case TypeWelcome:
		return "WELCOME"
	case TypeBye:
		return "BYE"
	default:
		return fmt.Sprintf("UNKNOWN-%#02x", byte(t))
	}
}

// Encode renders the frame into wire bytes: u8 type, u24 big-endian stream id,
// u32 big-endian payload length, payload.
func Encode(frame Frame) []byte {
	if frame.StreamID > MaxStreamID {
		panic("stream id exceeds 24 bits")
	}

	if len(frame.Payload) > MaxPayload {
		panic("frame payload exceeds limit")
	}

	result := make([]byte, HeaderSize+len(frame.Payload))
	binary.BigEndian.PutUint32(result[:typeStreamFieldSize], uint32(frame.Type)<<typeFieldShift|frame.StreamID)

	//nolint:gosec // bounded by the MaxPayload guard above
	binary.BigEndian.PutUint32(result[typeStreamFieldSize:HeaderSize], uint32(len(frame.Payload)))
	copy(result[HeaderSize:], frame.Payload)

	return result
}

// Decode parses exactly one complete frame, rejecting trailing bytes,
// oversized payloads, and unknown frame types.
func Decode(buf []byte) (Frame, error) {
	if len(buf) < HeaderSize {
		return Frame{}, ErrIncompleteFrame
	}

	if !knownType(Type(buf[0])) {
		return Frame{}, fmt.Errorf("%w: %#02x", ErrUnknownType, Type(buf[0]))
	}

	length := uint64(binary.BigEndian.Uint32(buf[4:HeaderSize]))
	if length > MaxPayload {
		return Frame{}, ErrPayloadTooLarge
	}

	full := HeaderSize + length

	switch {
	case full > uint64(len(buf)):
		return Frame{}, ErrIncompleteFrame
	case full < uint64(len(buf)):
		return Frame{}, ErrTrailingBytes
	}

	var payload []byte

	if length > 0 {
		payload = buf[HeaderSize:full]
	}

	return Frame{
		Type:     Type(buf[0]),
		StreamID: uint32(buf[1])<<u24HiShift | uint32(buf[2])<<u24LoShift | uint32(buf[3]),
		Payload:  payload,
	}, nil
}

// DecodeAll parses a complete batch of frames, enforcing the protocol batch
// limits and strict bounds checks on every frame boundary.
func DecodeAll(buf []byte) ([]Frame, error) {
	frames := make([]Frame, 0, initialBatchCapacity)

	for len(buf) != 0 {
		if len(frames) == MaxBatchFrames {
			return nil, ErrTooManyFrames
		}

		if len(buf) < HeaderSize {
			return nil, ErrIncompleteFrame
		}

		if !knownType(Type(buf[0])) {
			return nil, fmt.Errorf("%w: %#02x", ErrUnknownType, Type(buf[0]))
		}

		length := uint64(binary.BigEndian.Uint32(buf[4:HeaderSize]))
		if length > MaxPayload {
			return nil, ErrPayloadTooLarge
		}

		full := HeaderSize + length
		if full > uint64(len(buf)) {
			return nil, ErrIncompleteFrame
		}

		streamID := uint32(buf[1])<<u24HiShift | uint32(buf[2])<<u24LoShift | uint32(buf[3])
		frames = append(frames, Frame{
			Type:     Type(buf[0]),
			StreamID: streamID,
			Payload:  buf[HeaderSize:full],
		})
		buf = buf[full:]
	}

	if len(frames) == 0 {
		return nil, ErrEmptyBatch
	}

	return frames, nil
}

// WindowAmount decodes a nonzero u32 big-endian WINDOW delta.
func WindowAmount(payload []byte) (uint32, error) {
	if len(payload) != windowPayloadSize {
		return 0, ErrWindowAmount
	}

	value := binary.BigEndian.Uint32(payload)
	if value == 0 {
		return 0, ErrWindowAmount
	}

	return value, nil
}

// WindowPayload encodes a WINDOW delta.
func WindowPayload(amount uint32) []byte {
	result := make([]byte, windowPayloadSize)
	binary.BigEndian.PutUint32(result, amount)

	return result
}

// ValidateClient reports whether the frame may be sent by the client after
// bootstrap, mirroring the relay-side client-shape validation.
func ValidateClient(frame Frame) error {
	if frame.StreamID == 0 {
		if frame.Type != TypePong || len(frame.Payload) > MaxPongPayload {
			return fmt.Errorf("%w: type %#02x on stream zero", ErrInvalidShape, frame.Type)
		}

		return nil
	}

	switch frame.Type {
	case TypeOpen, TypeClose:
		if len(frame.Payload) != 0 {
			return fmt.Errorf("%w: type %#02x requires an empty payload", ErrInvalidShape, frame.Type)
		}
	case TypeData:
		if len(frame.Payload) == 0 {
			return fmt.Errorf("%w: DATA payload must be nonempty", ErrInvalidShape)
		}
	case TypeWindow:
		if _, err := WindowAmount(frame.Payload); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: type %#02x is invalid client-to-relay", ErrInvalidShape, frame.Type)
	}

	return nil
}

// ValidateServer reports whether the frame may be sent by the relay to the
// client after bootstrap.
func ValidateServer(frame Frame) error {
	if frame.StreamID == 0 {
		switch frame.Type {
		case TypePing, TypeBye:
			return nil
		case TypeWelcome:
			return nil
		default:
			return fmt.Errorf("%w: type %#02x on stream zero", ErrInvalidShape, frame.Type)
		}
	}

	switch frame.Type {
	case TypeClose:
		if len(frame.Payload) != 0 {
			return fmt.Errorf("%w: CLOSE requires an empty payload", ErrInvalidShape)
		}
	case TypeData:
		if len(frame.Payload) == 0 {
			return fmt.Errorf("%w: DATA payload must be nonempty", ErrInvalidShape)
		}
	case TypeWindow:
		if _, err := WindowAmount(frame.Payload); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: type %#02x is invalid relay-to-client", ErrInvalidShape, frame.Type)
	}

	return nil
}

func knownType(value Type) bool {
	switch value {
	case TypeOpen, TypeData, TypeClose, TypeWindow,
		TypePing, TypePong, TypeHello, TypeWelcome, TypeBye:
		return true
	default:
		return false
	}
}
