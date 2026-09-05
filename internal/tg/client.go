package tg

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/4q4r/teleparse/internal/config"

	"golang.org/x/time/rate"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
)

// primaryDC is the production DC used for connectivity probes.
const primaryDC = 2

// secondsPerMinute converts requests-per-minute into a per-second rate.
const secondsPerMinute = 60.0

// minFloodSleepThreshold is the smallest flood-sleep threshold the middleware
// honors: gotd's WithMaxWait(0) means "no limit", so a configured 0 must never
// reach it verbatim (a 1s floor parks every real wait; TDLib clamps flood
// arguments to >= 1s anyway).
const minFloodSleepThreshold = 1

// Creds is a resolved Telegram API credential pair. The cli layer resolves
// it through internal/config (environment first, credentials file second)
// and passes it down; tg never reads credential sources itself.
type Creds struct {
	APIID   int64
	APIHash string
}

// BuildMiddlewares assembles the request pipeline: floodwait always, plus the
// ratelimit token bucket only when Pacing.RequestsPerMinute is positive.
//
// The floodwait waiter sleeps inline only up to Pacing.FloodSleepThreshold;
// longer FLOOD_WAIT_%d errors (core.telegram.org/api/errors, code 420) are
// returned to the caller so the download manager can park the run — gotd's
// default waiter would otherwise retry indefinitely and silently freeze the
// CLI instead of recording a resumable parked run.
func BuildMiddlewares(pacing config.Pacing) []telegram.Middleware {
	threshold := pacing.FloodSleepThreshold
	if threshold < minFloodSleepThreshold {
		threshold = minFloodSleepThreshold
	}

	middlewares := []telegram.Middleware{
		floodwait.NewSimpleWaiter().WithMaxWait(time.Duration(threshold) * time.Second),
	}

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
	creds Creds,
	cfg *config.Config,
	paths *config.Paths,
	run func(ctx context.Context, client *telegram.Client) error,
) error {
	return RunWithUpdates(ctx, account, creds, cfg, paths, nil, run)
}

// RunWithUpdates is Run with a Telegram update handler attached to the client
// (QR login feeds its UpdateLoginToken approval signal through one); a nil
// handler keeps gotd's default no-updates mode.
func RunWithUpdates(
	ctx context.Context,
	account string,
	creds Creds,
	cfg *config.Config,
	paths *config.Paths,
	handler telegram.UpdateHandler,
	run func(ctx context.Context, client *telegram.Client) error,
) error {
	if creds.APIID == 0 || creds.APIHash == "" {
		return ErrCredsUnset
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

	client := telegram.NewClient(int(creds.APIID), creds.APIHash, telegram.Options{
		SessionStorage: storage,
		Resolver:       resolver,
		Middlewares:    BuildMiddlewares(cfg.Pacing),
		Device:         device,
		UpdateHandler:  handler,
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
	return ProbeDC(ctx, raw, primaryDC)
}
