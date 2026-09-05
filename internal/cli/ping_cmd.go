package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
)

// pingProbeTimeout bounds one DC ping (dial plus RPC round-trip).
const pingProbeTimeout = 10 * time.Second

// Sentinel errors for ping.
var (
	errUnknownPingDC  = errors.New("unknown DC")
	errAllPingsFailed = errors.New("every DC ping failed")
)

// Telegram production DC ids run 1 through 5.
const (
	dcIDMin = 1
	dcIDMax = 5
)

// resolvePingDCs parses the --dc value: "all" (default) expands to the
// probed DC set, a plain number selects one known DC.
func resolvePingDCs(value string) ([]int, error) {
	if value == "all" || value == "" {
		return probeDCs(), nil
	}

	dcID, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("--dc %q: want a DC number (%d-%d) or all: %w", value, dcIDMin, dcIDMax, err)
	}

	if dcID < dcIDMin || dcID > dcIDMax {
		return nil, fmt.Errorf("--dc %d: %w, want %d-%d or all", dcID, errUnknownPingDC, dcIDMin, dcIDMax)
	}

	return []int{dcID}, nil
}

// pingAllDCs measures connect time and RPC RTT to every DC through the
// effective proxy using the account session.
func pingAllDCs(ctx context.Context, client *telegram.Client, proxyURL string, dcIDs []int) []dcProbe {
	probes := make([]dcProbe, 0, len(dcIDs))

	for _, dcID := range dcIDs {
		probe := dcProbe{DC: dcID}

		probeCtx, cancel := context.WithTimeout(ctx, pingProbeTimeout)

		connectMs, rttMs, err := tg.PingDC(probeCtx, client, proxyURL, dcID)

		cancel()

		if err != nil {
			probe.Error = err.Error()
		} else {
			probe.ConnectMs, probe.RTTMs = connectMs, rttMs
		}

		probes = append(probes, probe)
	}

	return probes
}

func pingCmd(app *App) *cobra.Command {
	var dcArg string

	cmd := &cobra.Command{
		Use:   "ping [--dc N|all]",
		Short: "Measure connect time and RPC RTT to Telegram DCs",
		Long: "Pings the Telegram production DCs through the effective proxy using\n" +
			"the account session: connect is the transport dial time, rtt the\n" +
			"round-trip of one lightweight RPC on a pool dialed for that DC.",
		Example: "  teleparse ping\n  teleparse ping --dc 2\n  teleparse ping --dc all --format json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dcs, err := resolvePingDCs(dcArg)
			if err != nil {
				return fail(cmd, err)
			}

			var probes []dcProbe

			runErr := tg.Run(cmd.Context(), app.cfg.Auth.Account, app.cfg, app.paths, func(
				ctx context.Context,
				client *telegram.Client,
			) error {
				probes = pingAllDCs(ctx, client, app.cfg.Net.Proxy, dcs)

				return nil
			})
			if runErr != nil {
				return fail(cmd, runErr)
			}

			if err := renderProbes(cmd, app.outputFormat(), probes, proxySourceLabel(app.cfg.Net.ProxySource)); err != nil {
				return fail(cmd, err)
			}

			if allProbesFailed(probes) {
				return errAllPingsFailed
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&dcArg, "dc", "all", "data center id or all (2-5)")

	return cmd
}
