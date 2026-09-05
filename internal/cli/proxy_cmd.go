package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/spf13/cobra"
)

// proxyProbeTimeout bounds one DC probe dial.
const proxyProbeTimeout = 5 * time.Second

// dcProbe is one measured data-center result. RTTMs stays zero for
// connect-only probes (proxy test); ping fills it via a session RPC.
type dcProbe struct {
	DC        int    `json:"dc"`
	ConnectMs int64  `json:"connect_ms"`
	RTTMs     int64  `json:"rtt_ms"`
	Error     string `json:"error,omitempty"`
}

// probeDCs lists the production DCs proxy test and ping measure by
// default: the home DC plus the media DCs.
func probeDCs() []int {
	return []int{2, 3, 4, 5}
}

// proxySourceLabel renders Net.ProxySource for display, mapping the
// empty source (direct connection) to "direct".
func proxySourceLabel(source string) string {
	if source == "" {
		return "direct"
	}

	return source
}

// probeAllDCs measures the transport connect time to every DC through
// rawURL; no session is needed, so no RPC RTT is available.
func probeAllDCs(ctx context.Context, rawURL string, dcs []int) []dcProbe {
	probes := make([]dcProbe, 0, len(dcs))

	for _, dc := range dcs {
		probe := dcProbe{DC: dc}

		probeCtx, cancel := context.WithTimeout(ctx, proxyProbeTimeout)

		latency, err := tg.ProbeDC(probeCtx, rawURL, dc)

		cancel()

		if err != nil {
			probe.Error = err.Error()
		} else {
			probe.ConnectMs = latency.Milliseconds()
		}

		probes = append(probes, probe)
	}

	return probes
}

// allProbesFailed reports whether every DC probe errored.
func allProbesFailed(probes []dcProbe) bool {
	for _, probe := range probes {
		if probe.Error == "" {
			return false
		}
	}

	return len(probes) > 0
}

// renderProbes prints DC probes in the requested format.
func renderProbes(cmd *cobra.Command, format OutputFormat, probes []dcProbe, viaLabel string) error {
	switch format {
	case FormatJSON:
		return printJSON(cmd, struct {
			Results []dcProbe `json:"results"`
		}{Results: probes})
	case FormatPlain:
		rows := make([][]string, 0, len(probes)*2)

		for _, probe := range probes {
			rows = append(rows,
				[]string{fmt.Sprintf("dc%d.connect_ms", probe.DC), metricOrErr(probe.ConnectMs, probe.Error)},
				[]string{fmt.Sprintf("dc%d.rtt_ms", probe.DC), metricOrErr(probe.RTTMs, probe.Error)},
			)
		}

		return printPlainRows(cmd, []string{"metric", "value"}, rows)
	default:
		for _, probe := range probes {
			if err := printLine(cmd, "%s\n", probeLine(probe, viaLabel)); err != nil {
				return err
			}
		}

		return nil
	}
}

// metricOrErr renders a probe metric or its failure text.
func metricOrErr(value int64, errText string) string {
	if errText != "" {
		return errText
	}

	return strconv.FormatInt(value, 10)
}

// probeLine renders one table line: "dc2: connect=120ms rtt=80ms [via ...]"
// or the failure form.
func probeLine(probe dcProbe, viaLabel string) string {
	if probe.Error != "" {
		return fmt.Sprintf("dc%d: FAIL: %s [via %s]", probe.DC, probe.Error, viaLabel)
	}

	line := fmt.Sprintf("dc%d: connect=%dms", probe.DC, probe.ConnectMs)

	if probe.RTTMs > 0 {
		line += fmt.Sprintf(" rtt=%dms", probe.RTTMs)
	}

	return line + " [via " + viaLabel + "]"
}

// errAllProbesFailed reports a proxy test where every DC probe errored.
var errAllProbesFailed = errors.New("every DC probe failed")

func proxyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Inspect and test proxy configuration",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:     "show",
			Short:   "Print effective proxy settings",
			Example: "  teleparse proxy show",
			RunE: func(cmd *cobra.Command, _ []string) error {
				switch app.outputFormat() {
				case FormatJSON:
					return printJSON(cmd, struct {
						Proxy  string `json:"proxy"`
						Source string `json:"source"`
					}{Proxy: app.cfg.Net.Proxy, Source: proxySourceLabel(app.cfg.Net.ProxySource)})
				default:
					proxy := app.cfg.Net.Proxy
					if proxy == "" {
						proxy = "(direct)"
					}

					return printLine(cmd, "proxy: %s\nsource: %s\n",
						proxy, app.style.Dim(proxySourceLabel(app.cfg.Net.ProxySource)))
				}
			},
		},
		&cobra.Command{
			Use:     "test [URL]",
			Short:   "Probe proxy connectivity to Telegram DCs",
			Example: "  teleparse proxy test\n  teleparse proxy test socks5://127.0.0.1:1080",
			Args:    cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				proxyURL := app.cfg.Net.Proxy
				if len(args) == 1 {
					proxyURL = args[0]
				}

				label := proxyURL
				if label == "" {
					label = "(direct)"
				}

				// Validates and short-circuits unsupported schemes
				// (webproxy://) before any dial.
				if _, err := tg.ParseProxyURL(proxyURL); err != nil {
					return fail(cmd, fmt.Errorf("%s: %w", label, err))
				}

				probes := probeAllDCs(cmd.Context(), proxyURL, probeDCs())

				if err := renderProbes(cmd, app.outputFormat(), probes, proxySourceLabel(app.cfg.Net.ProxySource)); err != nil {
					return fail(cmd, err)
				}

				if allProbesFailed(probes) {
					return fail(cmd, fmt.Errorf("%s: %w", label, errAllProbesFailed))
				}

				return nil
			},
		},
	)

	return cmd
}
