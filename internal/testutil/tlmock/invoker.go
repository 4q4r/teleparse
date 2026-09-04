// Package tlmock provides an offline, documentation-accurate Telegram TL
// mock for smoke tests: a fake tg.Invoker whose dispatch table is keyed by
// the concrete request type, round-tripping every request and response
// through gotd's real binary TL codec. Fixture builders cite the constructor
// ids mirrored from core.telegram.org by gotd's generated types, and every
// recorded request is a decoded wire copy, so assertions read exactly what
// the production RPC stack would have transmitted.
package tlmock

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/gotd/td/bin"
	tg "github.com/gotd/td/tg"
)

// Sentinel errors wrapped by dispatch failures.
var (
	ErrNoHandler    = errors.New("tlmock: no handler registered for request")
	ErrNotPointer   = errors.New("tlmock: request must be a non-nil pointer")
	ErrDispatchType = errors.New("tlmock: handler received wrong request type")
	ErrNotDecoder   = errors.New("tlmock: request type does not implement bin.Decoder")
)

// Call records one invoked RPC: the concrete request type name plus a
// decoded copy of the request payload, rebuilt from the wire bytes so the
// record survives later mutations by caller or handler.
type Call struct {
	Type    string
	Request any
}

// handler adapts a typed fixture responder onto the untyped dispatch table.
type handler func(decoded any) (bin.Object, error)

// Mock is a fake tg.Invoker dispatching on the concrete request type.
// Handlers are registered with HandleFunc; every invoked request is recorded
// and retrievable through Requests or Recorded. The zero Mock is not usable;
// construct one with New.
type Mock struct {
	mu       sync.Mutex
	handlers map[reflect.Type]handler
	calls    []Call
}

// New returns a Mock with an empty dispatch table; any request invoked
// before a handler is registered fails loudly.
func New() *Mock {
	return &Mock{handlers: map[reflect.Type]handler{}}
}

// HandleFunc registers handle for every request whose concrete type is
// *TReq, replacing an earlier registration. The receiver is returned so
// registrations chain inside a single expression.
func HandleFunc[TReq any](mock *Mock, handle func(*TReq) (bin.Object, error)) *Mock {
	mock.handlers[reflect.TypeOf((*TReq)(nil))] = func(decoded any) (bin.Object, error) {
		request, ok := decoded.(*TReq)
		if !ok {
			return nil, fmt.Errorf("%w: want %T, got %T", ErrDispatchType, (*TReq)(nil), decoded)
		}

		return handle(request)
	}

	return mock
}

// Client wraps the mock in a real *tg.Client, so every generated API method
// round-trips through the dispatch table exactly as it would through the
// production RPC stack at the tg.Invoker seam.
func (m *Mock) Client() *tg.Client {
	return tg.NewClient(m)
}

// Recorded returns a copy of every recorded call in invocation order.
func (m *Mock) Recorded() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Call, len(m.calls))
	copy(out, m.calls)

	return out
}

// Requests returns the recorded requests whose concrete type is *TReq, in
// invocation order; requests of other types are skipped.
func Requests[TReq any](mock *Mock) []*TReq {
	mock.mu.Lock()
	defer mock.mu.Unlock()

	out := make([]*TReq, 0, len(mock.calls))

	for idx := range mock.calls {
		if request, ok := mock.calls[idx].Request.(*TReq); ok {
			out = append(out, request)
		}
	}

	return out
}

// Invoke implements tg.Invoker: the request is encoded to its wire form and
// decoded into fresh copies for the dispatch table and the call record, the
// handler's response object is encoded to its wire form and decoded into
// the caller's output — proving every fixture survives the real TL codec.
func (m *Mock) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	requestType := reflect.TypeOf(input)

	if requestType == nil || requestType.Kind() != reflect.Pointer {
		return fmt.Errorf("%w, got %v", ErrNotPointer, requestType)
	}

	handle, ok := m.handlers[requestType]
	if !ok {
		return fmt.Errorf("%w %s", ErrNoHandler, requestType)
	}

	var wire bin.Buffer

	if err := input.Encode(&wire); err != nil {
		return fmt.Errorf("tlmock: encode request %s: %w", requestType, err)
	}

	requestCopy, err := decodeRequest(requestType, wire.Copy())
	if err != nil {
		return fmt.Errorf("tlmock: decode request %s: %w", requestType, err)
	}

	recordCopy, err := decodeRequest(requestType, wire.Copy())
	if err != nil {
		return fmt.Errorf("tlmock: decode record %s: %w", requestType, err)
	}

	m.record(requestType.String(), recordCopy)

	result, err := handle(requestCopy)
	if err != nil {
		return err
	}

	var responseWire bin.Buffer

	if err := result.Encode(&responseWire); err != nil {
		return fmt.Errorf("tlmock: encode response for %s: %w", requestType, err)
	}

	if err := output.Decode(&responseWire); err != nil {
		return fmt.Errorf("tlmock: decode response for %s: %w", requestType, err)
	}

	return nil
}

func (m *Mock) record(typeName string, request any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, Call{Type: typeName, Request: request})
}

// decodeRequest rebuilds a fresh *TReq from an encoded wire payload.
func decodeRequest(requestType reflect.Type, payload []byte) (any, error) {
	fresh, ok := reflect.New(requestType.Elem()).Interface().(bin.Decoder)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotDecoder, requestType)
	}

	var wire bin.Buffer

	wire.ResetTo(payload)

	if err := fresh.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode %s: %w", requestType, err)
	}

	return fresh, nil
}
