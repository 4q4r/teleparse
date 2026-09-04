//go:build webproxy_integration

package webproxy_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"teleparse/internal/webproxy"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/mtproxy/obfuscated2"
	"github.com/gotd/td/telegram/dcs"
	"github.com/stretchr/testify/require"
)

// The integration test drives the reference telegramdesktop/tproxy-server.
// No official container image is published (Docker Hub repository missing,
// GHCR requires authentication), so the server is cloned and built from
// source per the deployment README's manual path. The relay runs on
// 127.0.0.1 which satisfies its loopback-proxy client check, and every
// request carries the canonical Host header like the production Caddy front
// would.

const (
	integrationHost    = "relay.integration.test"
	integrationSecret  = "000102030405060708090a0b0c0d0e0f"
	integrationRepo    = "https://github.com/telegramdesktop/tproxy-server"
	integrationRelayLn = "127.0.0.1:18080"
	integrationAdminLn = "127.0.0.1:18081"
)

type integrationLimits struct {
	MaxHeaderBytes          int `json:"max_header_bytes"`
	MaxBodyBytes            int `json:"max_body_bytes"`
	MaxFramePayload         int `json:"max_frame_payload"`
	CarrierBatchBytes       int `json:"carrier_batch_bytes"`
	MaxStreamsPerSession    int `json:"max_streams_per_session"`
	MaxClosedStreamIds      int `json:"max_closed_stream_ids"`
	MaxPendingPerSession    int `json:"max_pending_per_session"`
	MaxPendingGlobal        int `json:"max_pending_global"`
	MaxPendingItemsSession  int `json:"max_pending_items_per_session"`
	MaxPendingItemsGlobal   int `json:"max_pending_items_global"`
	MaxSessionsPerIp        int `json:"max_sessions_per_ip"`
	MaxSessionsGlobal       int `json:"max_sessions_global"`
	MaxStreamsGlobal        int `json:"max_streams_global"`
	MaxBackendDialsInFlight int `json:"max_backend_dials_in_flight"`
	NewSessionsPerMinute    int `json:"new_sessions_per_minute"`
	NewSessionsBurst        int `json:"new_sessions_burst"`
	NewStreamsPerMinute     int `json:"new_streams_per_minute"`
	NewStreamsBurst         int `json:"new_streams_burst"`
	MaxBootstrapsPerIp      int `json:"max_bootstraps_per_ip"`
	MaxBootstrapsGlobal     int `json:"max_bootstraps_global"`
	NewBootstrapsPerMinute  int `json:"new_bootstraps_per_minute"`
	NewBootstrapsBurst      int `json:"new_bootstraps_burst"`
	MaxProfiles             int `json:"max_profiles"`
}

type integrationTimeouts struct {
	BackendDial       string `json:"backend_dial"`
	LongPoll          string `json:"long_poll"`
	ReconnectGrace    string `json:"reconnect_grace"`
	BootstrapLifetime string `json:"bootstrap_lifetime"`
	ReadHeader        string `json:"read_header"`
	Idle              string `json:"idle"`
	Shutdown          string `json:"shutdown"`
}

type integrationConfig struct {
	PublicHostname string              `json:"public_hostname"`
	Listen         string              `json:"listen"`
	AdminListen    string              `json:"admin_listen"`
	PublicDir      string              `json:"public_dir"`
	ProfilesFile   string              `json:"profiles_file"`
	TokenKeyFile   string              `json:"token_key_file"`
	Limits         integrationLimits   `json:"limits"`
	Timeouts       integrationTimeouts `json:"timeouts"`
}

type integrationProfiles struct {
	Profiles []integrationProfile `json:"profiles"`
}

type integrationProfile struct {
	Name        string `json:"name"`
	Secret      string `json:"secret"`
	Backend     string `json:"backend"`
	CarrierMode string `json:"carrier_mode"`
}

// integrationMTProxy echoes obfuscated2 bytes back like a stock MTProxy
// would echo a MTProto round trip at the transport level.
func integrationMTProxy(t *testing.T, secret []byte) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				rw, _, err := obfuscated2.Accept(conn, secret)
				if err != nil {
					return
				}

				_, _ = io.Copy(rw, rw)
			}()
		}
	}()

	return listener.Addr().String()
}

// buildReferenceServer clones and builds the upstream relay binary.
func buildReferenceServer(t *testing.T) string {
	t.Helper()

	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to build the reference tproxy-server", tool)
		}
	}

	workdir := filepath.Join(os.TempDir(), fmt.Sprintf("tproxy-server-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(workdir) })

	clone := exec.Command("git", "clone", "--depth", "1", integrationRepo, workdir)
	clone.Stderr = os.Stderr
	if err := clone.Run(); err != nil {
		t.Skipf("cannot clone the reference server: %v", err)
	}

	binary := filepath.Join(workdir, "tproxy-server.bin")
	build := exec.Command("go", "build", "-o", binary, "./cmd/tproxy-server")
	build.Dir = workdir
	build.Stderr = os.Stderr
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(workdir, ".gocache"))
	if err := build.Run(); err != nil {
		t.Skipf("cannot build the reference server: %v", err)
	}

	return binary
}

func writeIntegrationConfig(t *testing.T, binary, backend string) string {
	t.Helper()

	configDir := filepath.Join(filepath.Dir(binary), "config")
	require.NoError(t, os.MkdirAll(configDir, 0o700))

	publicDir := filepath.Join(configDir, "public")
	require.NoError(t, os.MkdirAll(publicDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(publicDir, "index.html"),
		[]byte("<html><body>integration site</body></html>"), 0o600))

	profilesPath := filepath.Join(configDir, "profiles.json")
	profiles := integrationProfiles{Profiles: []integrationProfile{{
		Name:        "default",
		Secret:      integrationSecret,
		Backend:     backend,
		CarrierMode: "websocket",
	}}}

	encoded, err := json.Marshal(profiles)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilesPath, encoded, 0o600))

	tokenKey := make([]byte, 32)
	_, err = rand.Read(tokenKey)
	require.NoError(t, err)
	tokenKeyPath := filepath.Join(configDir, "token.key")
	require.NoError(t, os.WriteFile(tokenKeyPath, tokenKey, 0o600))

	configPath := filepath.Join(configDir, "config.json")
	config := integrationConfig{
		PublicHostname: integrationHost,
		Listen:         integrationRelayLn,
		AdminListen:    integrationAdminLn,
		PublicDir:      publicDir,
		ProfilesFile:   profilesPath,
		TokenKeyFile:   tokenKeyPath,
		Limits: integrationLimits{
			MaxHeaderBytes: 16384, MaxBodyBytes: 2097152, MaxFramePayload: 1048576,
			CarrierBatchBytes: 2097152, MaxStreamsPerSession: 128,
			MaxClosedStreamIds: 4096, MaxPendingPerSession: 33554432,
			MaxPendingGlobal: 536870912, MaxPendingItemsSession: 16384,
			MaxPendingItemsGlobal: 262144, MaxSessionsGlobal: 128,
			MaxStreamsGlobal: 4096, MaxBackendDialsInFlight: 256,
			NewSessionsPerMinute: 600, NewSessionsBurst: 128,
			NewStreamsPerMinute: 6000, NewStreamsBurst: 512,
			MaxBootstrapsGlobal: 512, NewBootstrapsPerMinute: 1200,
			NewBootstrapsBurst: 256, MaxProfiles: 32,
		},
		Timeouts: integrationTimeouts{
			BackendDial: "5s", LongPoll: "25s", ReconnectGrace: "2m",
			BootstrapLifetime: "2m", ReadHeader: "10s", Idle: "75s", Shutdown: "15s",
		},
	}

	encoded, err = json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, encoded, 0o600))

	return configPath
}

func TestWebProxyIntegrationAgainstReferenceServer(t *testing.T) {
	secret, err := hex.DecodeString(integrationSecret)
	require.NoError(t, err)

	backend := integrationMTProxy(t, secret)
	binary := buildReferenceServer(t)
	configPath := writeIntegrationConfig(t, binary, backend)

	server := exec.Command(binary, "-config", configPath)
	server.Stderr = os.Stderr
	require.NoError(t, server.Start())
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})

	adminURL := "http://" + integrationAdminLn
	require.Eventually(t, func() bool {
		response, err := http.Get(adminURL + "/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()

		return response.StatusCode == http.StatusOK
	}, 30*time.Second, 200*time.Millisecond)

	resolver, err := webproxy.NewResolver(webproxy.Config{
		Host:    integrationHost,
		Secret:  secret,
		BaseURL: "http://" + integrationRelayLn,
		Client:  &http.Client{},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var asResolver dcs.Resolver = resolver
	conn, err := asResolver.Primary(ctx, 2, dcs.List{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	request1 := strings.Repeat("integration-", 3) // 36 bytes, 4-aligned for MTProto
	payload := &bin.Buffer{Buf: []byte(request1)}
	require.NoError(t, conn.Send(ctx, payload))

	received := &bin.Buffer{}
	require.NoError(t, conn.Recv(ctx, received))
	require.Equal(t, request1, string(received.Buf))

	second := &bin.Buffer{Buf: []byte(strings.Repeat("frame-two-", 40))}
	require.NoError(t, conn.Send(ctx, second))

	again := &bin.Buffer{}
	require.NoError(t, conn.Recv(ctx, again))
	require.Equal(t, strings.Repeat("frame-two-", 40), string(again.Buf))
}
