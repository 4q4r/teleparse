package cli

import (
	"context"
	"errors"
	"fmt"
	"teleparse/internal/tg"
	"time"

	"github.com/spf13/cobra"
)

// proxyProbeTimeout bounds the proxy test dial.
const proxyProbeTimeout = 5 * time.Second

func proxyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Inspect and test proxy configuration",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Print effective proxy settings",
			RunE: func(cmd *cobra.Command, _ []string) error {
				proxy := app.cfg.Net.Proxy
				if proxy == "" {
					proxy = "(direct)"
				}

				return printLine(cmd, "proxy: %s\n", proxy)
			},
		},
		&cobra.Command{
			Use:   "test [URL]",
			Short: "Probe proxy connectivity to Telegram DCs",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				proxyURL := app.cfg.Net.Proxy
				if len(args) == 1 {
					proxyURL = args[0]
				}

				label := proxyURL
				if label == "" {
					label = "(direct)"
				}

				ctx, cancel := context.WithTimeout(cmd.Context(), proxyProbeTimeout)
				defer cancel()

				latency, err := tg.ProbeProxy(ctx, proxyURL)
				if err != nil {
					if errors.Is(err, tg.ErrWebProxyNotWired) {
						return fail(cmd, fmt.Errorf("%s: %w", label, tg.ErrWebProxyNotWired))
					}

					return fail(cmd, fmt.Errorf("%s: FAIL: %w", label, err))
				}

				return printLine(cmd, "%s: OK (%s)\n", label, latency.Round(time.Millisecond))
			},
		},
	)

	return cmd
}
