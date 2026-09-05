package tg

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/4q4r/teleparse/internal/webproxy"

	"github.com/gotd/td/mtproxy"
	"github.com/gotd/td/telegram/dcs"
	"golang.org/x/net/proxy"
)

// ParseProxyURL turns a proxy URL into a gotd DC resolver. An empty string
// yields the default direct resolver. Supported schemes: socks5, socks4, http,
// mtproto and webproxy.
//
//nolint:ireturn // the resolver abstraction is exactly what telegram.Options needs.
func ParseProxyURL(raw string) (dcs.Resolver, error) {
	if raw == "" {
		return dcs.DefaultResolver(), nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q: %w: %w", raw, ErrBadProxyURL, err)
	}

	if parsed.Host == "" {
		return nil, fmt.Errorf("%q: %w: empty host", raw, ErrBadProxyURL)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "socks5":
		return plainResolver(socks5DialFunc(parsed)), nil
	case "socks4":
		return plainResolver(Socks4DialFunc(parsed.Host, parsed.User.Username())), nil
	case "http":
		return plainResolver(HTTPConnectDialFunc(parsed.Host, parsed.User.Username(), userPassword(parsed))), nil
	case "mtproto":
		return mtProtoResolver(parsed)
	case "webproxy":
		return webProxyResolver(parsed)
	default:
		return nil, fmt.Errorf("%q: %w", raw, ErrBadProxyScheme)
	}
}

// webProxyResolver validates webproxy://host[:port]/SECRET?carrier=auto|websocket|https
// and returns the WEB-proxy carrier resolver; the session bootstraps lazily
// on first dial, so construction performs no network I/O.
//
//nolint:ireturn // the resolver abstraction is exactly what telegram.Options needs.
func webProxyResolver(parsed *url.URL) (dcs.Resolver, error) {
	encoded := strings.TrimPrefix(parsed.Path, "/")
	if encoded == "" {
		return nil, fmt.Errorf("%q: %w: missing secret", parsed.String(), ErrBadProxySecret)
	}

	secret, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%q: %w: %w", encoded, ErrBadProxySecret, err)
	}

	mode, err := carrierMode(parsed.Query().Get("carrier"))
	if err != nil {
		return nil, fmt.Errorf("%q: %w", parsed.String(), err)
	}

	resolver, err := webproxy.NewResolver(webproxy.Config{
		Host:        parsed.Host,
		Secret:      secret,
		CarrierMode: mode,
	})
	if err != nil {
		return nil, fmt.Errorf("webproxy %s: %w: %w", parsed.Host, ErrBadProxySecret, err)
	}

	return resolver, nil
}

// carrierMode maps the carrier query parameter onto webproxy.CarrierMode;
// the empty value selects auto.
func carrierMode(raw string) (webproxy.CarrierMode, error) {
	if raw == "" {
		return webproxy.ModeAuto, nil
	}

	mode := webproxy.CarrierMode(raw)
	if mode != webproxy.ModeAuto && !mode.Offered() {
		return "", fmt.Errorf("carrier %q: %w (want auto|websocket|https)", raw, ErrBadProxyURL)
	}

	return mode, nil
}

// plainResolver wraps a dial function into the plain TCP resolver.
//
//nolint:ireturn // the resolver abstraction is exactly what telegram.Options needs.
func plainResolver(dial dcs.DialFunc) dcs.Resolver {
	return dcs.Plain(dcs.PlainOptions{Dial: dial})
}

// socks5DialFunc builds a SOCKS5 dial function from a parsed proxy URL.
func socks5DialFunc(parsed *url.URL) dcs.DialFunc {
	var auth *proxy.Auth

	if parsed.User != nil {
		password, _ := parsed.User.Password()
		auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
	}

	dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
	if err != nil {
		return failingDial(fmt.Errorf("socks5 %s: %w: %w", parsed.Host, ErrBadProxyURL, err))
	}

	return contextDial(dialer)
}

// mtProtoResolver validates the hex secret (dd-padded and ee fake-TLS forms
// included) and returns the MTProxy resolver.
//
//nolint:ireturn // the resolver abstraction is exactly what telegram.Options needs.
func mtProtoResolver(parsed *url.URL) (dcs.Resolver, error) {
	encoded := strings.TrimPrefix(parsed.Path, "/")
	if encoded == "" {
		return nil, fmt.Errorf("%q: %w: missing secret", parsed.String(), ErrBadProxySecret)
	}

	secret, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%q: %w: %w", encoded, ErrBadProxySecret, err)
	}

	if _, err := mtproxy.ParseSecret(secret); err != nil {
		return nil, fmt.Errorf("%q: %w: %w", encoded, ErrBadProxySecret, err)
	}

	resolver, err := dcs.MTProxy(parsed.Host, secret, dcs.MTProxyOptions{})
	if err != nil {
		return nil, fmt.Errorf("mtproxy %s: %w: %w", parsed.Host, ErrBadProxySecret, err)
	}

	return resolver, nil
}

// contextDial adapts a proxy.Dialer to a context-aware dial function,
// preferring the native DialContext when the dialer implements it.
func contextDial(dialer proxy.Dialer) dcs.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if contextual, ok := dialer.(proxy.ContextDialer); ok {
			return contextual.DialContext(ctx, network, addr)
		}

		type dialResult struct {
			conn net.Conn
			err  error
		}

		results := make(chan dialResult, 1)

		go func() {
			conn, err := dialer.Dial(network, addr)
			results <- dialResult{conn: conn, err: err}
		}()

		select {
		case res := <-results:
			return res.conn, res.err
		case <-ctx.Done():
			return nil, fmt.Errorf("dial %s: %w", addr, ctx.Err())
		}
	}
}

// failingDial returns a dial function that always reports the given error,
// keeping dialer construction total.
func failingDial(err error) dcs.DialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, err
	}
}

// userPassword extracts the password from proxy URL userinfo.
func userPassword(parsed *url.URL) string {
	if parsed.User == nil {
		return ""
	}

	password, _ := parsed.User.Password()

	return password
}
