package tg

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"teleparse/internal/config"
	"time"

	"golang.org/x/time/rate"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
)

// primaryDC is the production DC used for connectivity probes.
const primaryDC = 2

// Environment variables carrying the Telegram API credentials.
const (
	envAPIID   = "TELEPARSE_API_ID"
	envAPIHash = "TELEPARSE_API_HASH"
)

// secondsPerMinute converts requests-per-minute into a per-second rate.
const secondsPerMinute = 60.0

// CredsFromEnv reads TELEPARSE_API_ID and TELEPARSE_API_HASH, failing with
// ErrAPICredsMissing when either is unset.
func CredsFromEnv() (int64, string, error) {
	rawID, rawHash := os.Getenv(envAPIID), os.Getenv(envAPIHash)
	if rawID == "" || rawHash == "" {
		return 0, "", fmt.Errorf("%s/%s: %w", envAPIID, envAPIHash, ErrAPICredsMissing)
	}

	apiID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%s %q: %w: %w", envAPIID, rawID, ErrAPICredsMissing, err)
	}

	return apiID, rawHash, nil
}

// BuildMiddlewares assembles the request pipeline: floodwait always, plus the
// ratelimit token bucket only when Pacing.RequestsPerMinute is positive.
func BuildMiddlewares(pacing config.Pacing) []telegram.Middleware {
	middlewares := []telegram.Middleware{floodwait.NewSimpleWaiter()}

	if pacing.RequestsPerMinute > 0 {
		perSecond := rate.Limit(float64(pacing.RequestsPerMinute) / secondsPerMinute)
		middlewares = append(middlewares, ratelimit.New(perSecond, pacing.RequestsPerMinute))
	}

	return middlewares
}

// Run locks the account, builds a telegram.Client from cfg (session storage,
// proxy resolver, pacing middlewares, stable device identity) and executes fn
// inside client.Run. The account lock is held until fn returns.
func Run(
	ctx context.Context,
	account string,
	cfg *config.Config,
	paths *config.Paths,
	run func(ctx context.Context, client *telegram.Client) error,
) error {
	apiID, apiHash, err := CredsFromEnv()
	if err != nil {
		return err
	}

	manager := NewAccountManager(paths.AccountsDir)

	storage, err := manager.Storage(account)
	if err != nil {
		return err
	}

	device, err := manager.Device(account)
	if err != nil {
		return err
	}

	resolver, err := ParseProxyURL(cfg.Net.Proxy)
	if err != nil {
		return err
	}

	lock, err := manager.Lock(account)
	if err != nil {
		return err
	}

	defer func() {
		if err := lock.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "warning: release account lock:", err)
		}
	}()

	client := telegram.NewClient(int(apiID), apiHash, telegram.Options{
		SessionStorage: storage,
		Resolver:       resolver,
		Middlewares:    BuildMiddlewares(cfg.Pacing),
		Device:         device,
	})

	if err := client.Run(ctx, func(ctx context.Context) error {
		return run(ctx, client)
	}); err != nil {
		return fmt.Errorf("run telegram client: %w", err)
	}

	return nil
}

// ProbeProxy dials the primary production DC through the given proxy URL
// ("" = direct) and reports the round-trip latency.
func ProbeProxy(ctx context.Context, raw string) (time.Duration, error) {
	resolver, err := ParseProxyURL(raw)
	if err != nil {
		return 0, err
	}

	started := time.Now()

	conn, err := resolver.Primary(ctx, primaryDC, dcs.Prod())
	if err != nil {
		return 0, fmt.Errorf("dial DC %d via %q: %w", primaryDC, raw, err)
	}

	if err := conn.Close(); err != nil {
		return 0, fmt.Errorf("close probe connection: %w", err)
	}

	return time.Since(started), nil
}
