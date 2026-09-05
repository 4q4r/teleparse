package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/spf13/cobra"
)

// errUnreachable marks a failed pre-flight connectivity probe.
var errUnreachable = errors.New("cannot reach Telegram — check network or proxy settings (net.proxy, TELEPARSE_PROXY)")

// connectivityProbeTimeout bounds the pre-flight connection check.
const connectivityProbeTimeout = 6 * time.Second

// runConnectivityCheck always runs before any server interaction: it proves
// the network path to Telegram (through the effective proxy) so problems
// surface before any work starts. The one-line status goes to stderr so
// machine-readable stdout (--format json, --count-only) stays clean; it is
// suppressed under silent mode, but the probe itself always runs.
func runConnectivityCheck(cmd *cobra.Command, app *App) error {
	probeCtx, cancel := context.WithTimeout(cmd.Context(), connectivityProbeTimeout)
	defer cancel()

	latency, err := tg.ProbeProxy(probeCtx, app.cfg.Net.Proxy)
	if err != nil {
		return fmt.Errorf("connection check failed: %w: %w", err, errUnreachable)
	}

	if app.silentMode(cmd) {
		return nil
	}

	via := app.cfg.Net.ProxySource
	if via == "" {
		via = "direct"
	}

	line := fmt.Sprintf("connection: OK (dc2, %dms, %s)\n", latency.Milliseconds(), via)

	if _, err := fmt.Fprint(cmd.ErrOrStderr(), line); err != nil {
		return fmt.Errorf("print connection status: %w", err)
	}

	return nil
}
