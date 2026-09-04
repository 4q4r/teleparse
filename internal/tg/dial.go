package tg

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gotd/td/telegram/dcs"
)

// SOCKS4 wire constants.
const (
	socks4Version     byte = 0x04
	socks4Command     byte = 0x01
	socks4ReplyLen         = 8
	socks4RequestHead      = 8
	socks4Granted     byte = 0x5A
	nulTerminator     byte = 0x00
	ipv4Bytes              = 4
)

// handshakeDeadline bounds a single proxy handshake when the context has none.
const handshakeDeadline = 15 * time.Second

// Socks4DialFunc returns a dial function speaking the classic SOCKS4 protocol
// (no DNS through the proxy: hostnames are resolved locally to IPv4).
func Socks4DialFunc(proxyAddr, userID string) dcs.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portText, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("socks4 target %q: %w: %w", addr, ErrBadProxyURL, err)
		}

		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("socks4 target port %q: %w", portText, ErrBadProxyURL)
		}

		destIP, err := resolveIPv4(ctx, host)
		if err != nil {
			return nil, err
		}

		conn, err := (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("socks4 dial proxy %s: %w", proxyAddr, err)
		}

		if err := socks4Handshake(conn, destIP, port, userID); err != nil {
			_ = conn.Close()

			return nil, err
		}

		return conn, nil
	}
}

// socks4Handshake writes the CONNECT request and validates the 8-byte reply.
func socks4Handshake(conn net.Conn, destIP net.IP, port int, userID string) error {
	if err := conn.SetDeadline(time.Now().Add(handshakeDeadline)); err != nil {
		return fmt.Errorf("socks4 deadline: %w", err)
	}

	request := make([]byte, 0, socks4RequestHead+len(userID)+1)
	request = append(request, socks4Version, socks4Command)

	var portBytes [2]byte

	binary.BigEndian.PutUint16(portBytes[:], uint16(port)) //nolint:gosec // port validated in 1..65535 above
	request = append(request, portBytes[:]...)
	request = append(request, destIP.To4()...)
	request = append(request, userID...)
	request = append(request, nulTerminator)

	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("socks4 send request: %w", err)
	}

	reply := make([]byte, socks4ReplyLen)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks4 read reply: %w", err)
	}

	if reply[1] != socks4Granted {
		return fmt.Errorf("socks4 code 0x%02X: %w", reply[1], ErrProxyRefused)
	}

	return nil
}

// resolveIPv4 resolves host to an IPv4 address, rejecting IPv6 literals.
func resolveIPv4(ctx context.Context, host string) (net.IP, error) {
	destIP := net.ParseIP(host)
	if destIP != nil {
		if destIP.To4() == nil {
			return nil, fmt.Errorf("socks4 target %s: %w: IPv6 unsupported", host, ErrBadProxyURL)
		}

		return destIP, nil
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, fmt.Errorf("socks4 resolve %s: %w", host, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("socks4 resolve %s: %w: no IPv4 address", host, ErrBadProxyURL)
	}

	return ips[0], nil
}

// HTTPConnectDialFunc returns a dial function speaking HTTP CONNECT with
// optional basic proxy authentication.
func HTTPConnectDialFunc(proxyAddr, username, password string) dcs.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("http target %q: %w: %w", addr, ErrBadProxyURL, err)
		}

		conn, err := (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("http dial proxy %s: %w", proxyAddr, err)
		}

		if err := httpConnectHandshake(conn, addr, username, password); err != nil {
			_ = conn.Close()

			return nil, err
		}

		return conn, nil
	}
}

// httpConnectHandshake sends CONNECT and requires a 2xx response.
func httpConnectHandshake(conn net.Conn, addr, username, password string) error {
	if err := conn.SetDeadline(time.Now().Add(handshakeDeadline)); err != nil {
		return fmt.Errorf("http deadline: %w", err)
	}

	request := "CONNECT " + addr + " HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n"

	if username != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		request += "Proxy-Authorization: Basic " + creds + "\r\n"
	}

	request += "\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		return fmt.Errorf("http send CONNECT: %w", err)
	}

	target := &url.URL{Opaque: addr}
	req := &http.Request{Method: http.MethodConnect, URL: target, Host: addr}

	response, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return fmt.Errorf("http read CONNECT reply: %w", err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %s: %w", response.Status, ErrProxyRefused)
	}

	return nil
}
