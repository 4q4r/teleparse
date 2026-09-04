package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"teleparse/internal/config"
	"teleparse/internal/tg"
	"time"

	"github.com/spf13/cobra"
)

// doctorLimits and permissions bound the environment checks.
const (
	minFreeBytes   = 100 << 20
	probeTimeout   = 5 * time.Second
	doctorDirPerm  = 0o700
	doctorFilePerm = 0o600
)

// minFreeMiB renders minFreeBytes for messages so the threshold lives in
// exactly one place.
const minFreeMiB = minFreeBytes >> 20

// Sentinel errors wrapped by dynamic doctor messages.
var errDoctorFailed = errors.New("one or more doctor checks failed")

// errLowDiskSpace reports the free-bytes floor; the threshold itself is
// rendered from minFreeBytes at the call site.
var errLowDiskSpace = fmt.Errorf("less than %d MiB free under the downloads root", minFreeMiB)

// doctorCheck is one PASS/FAIL line; hint carries the FAIL suggestion,
// note an extra line printed after PASS and skip renders a non-failing
// SKIP line when the check cannot run.
type doctorCheck struct {
	name string
	err  error
	hint string
	note string
	skip string
}

func doctorCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, API credentials, session, disk, database",
		Example: "  teleparse doctor\n" +
			"  teleparse doctor --config /path/to/config.toml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := []func() doctorCheck{
				checkAPICreds,
				func() doctorCheck { return checkWritable("accounts dir", app.paths.AccountsDir) },
				func() doctorCheck { return checkWritable("state db dir", filepath.Dir(app.paths.StateDB)) },
				func() doctorCheck { return checkDownloads(app.paths.Downloads) },
				func() doctorCheck {
					return checkProxy(app.cfg.Net.Proxy, proxySourceLabel(app.cfg.Net.ProxySource), app.cfg.Net.IgnoreEnv)
				},
				func() doctorCheck {
					return checkPing(app.cfg.Net.Proxy, proxySourceLabel(app.cfg.Net.ProxySource))
				},
			}

			premium := checkPremium(app)

			if err := printLine(cmd, "config:  %s\naccounts: %s\npremium: %s (source: %s)\n",
				configPath(cmd), app.paths.AccountsDir, yesNo(premium.Premium), premium.Source); err != nil {
				return fail(cmd, err)
			}

			failed := 0

			for _, check := range checks {
				result := check()

				if result.skip != "" {
					if err := printLine(cmd, "SKIP %s: %s\n", result.name, result.skip); err != nil {
						return fail(cmd, err)
					}

					continue
				}

				if result.err != nil {
					failed++

					if err := printLine(cmd, "FAIL %s: %v\n", result.name, result.err); err != nil {
						return fail(cmd, err)
					}

					if result.hint != "" {
						if err := printLine(cmd, "     fix: %s\n", result.hint); err != nil {
							return fail(cmd, err)
						}
					}

					continue
				}

				if err := printLine(cmd, "PASS %s\n", result.name); err != nil {
					return fail(cmd, err)
				}

				if result.note != "" {
					if err := printLine(cmd, "     note: %s\n", result.note); err != nil {
						return fail(cmd, err)
					}
				}
			}

			if failed > 0 {
				return fmt.Errorf("%d: %w", failed, errDoctorFailed)
			}

			return nil
		},
	}
}

// checkPremium resolves the default account's premium state from the
// account cache alone; doctor never opens a session.
func checkPremium(app *App) tg.PremiumStatus {
	return tg.NewAccountManager(app.paths.AccountsDir).AccountPremium(context.Background(), app.cfg.Auth.Account, nil)
}

// checkPing measures the transport RTT to the primary DC through the
// effective proxy, best-effort: network failures SKIP, never FAIL.
func checkPing(proxyURL, source string) doctorCheck {
	if proxyURL == "" {
		return doctorCheck{
			name: "ping dc2 [direct]",
			skip: "direct connection: configure a proxy to measure latency",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	latency, err := tg.ProbeProxy(ctx, proxyURL)
	if err != nil {
		return doctorCheck{name: "ping dc2 [via proxy " + source + "]", skip: err.Error()}
	}

	return doctorCheck{
		name: "ping dc2 [via proxy " + source + "]",
		note: "connect " + latency.Round(time.Millisecond).String(),
	}
}

func checkAPICreds() doctorCheck {
	if _, _, err := tg.CredsFromEnv(); err != nil {
		return doctorCheck{
			name: "api credentials", err: err,
			hint: "export TELEPARSE_API_ID=... TELEPARSE_API_HASH=... (create them at https://my.telegram.org)",
		}
	}

	return doctorCheck{name: "api credentials"}
}

func checkWritable(name, dir string) doctorCheck {
	if err := os.MkdirAll(dir, doctorDirPerm); err != nil {
		return doctorCheck{
			name: name, err: fmt.Errorf("create %s: %w", dir, err),
			hint: "check ownership and permissions of the parent directory",
		}
	}

	probe := filepath.Join(dir, ".teleparse-doctor")

	if err := os.WriteFile(probe, nil, doctorFilePerm); err != nil {
		return doctorCheck{
			name: name, err: fmt.Errorf("write %s: %w", probe, err),
			hint: "check filesystem permissions (or free space if the disk is full)",
		}
	}

	if err := os.Remove(probe); err != nil {
		return doctorCheck{name: name, err: fmt.Errorf("clean %s: %w", probe, err)}
	}

	return doctorCheck{name: name}
}

func checkDownloads(root string) doctorCheck {
	if err := os.MkdirAll(root, doctorDirPerm); err != nil {
		return doctorCheck{
			name: "downloads root", err: fmt.Errorf("create %s: %w", root, err),
			hint: "check ownership and permissions of the parent directory",
		}
	}

	var stats syscall.Statfs_t

	if err := syscall.Statfs(root, &stats); err != nil {
		return doctorCheck{name: "downloads root", err: fmt.Errorf("statfs %s: %w", root, err)}
	}

	free := uint64(0)
	if stats.Bsize > 0 {
		free = stats.Bavail * uint64(stats.Bsize)
	}

	if free < minFreeBytes {
		return doctorCheck{
			name: "downloads root", err: fmt.Errorf("%d bytes: %w", free, errLowDiskSpace),
			hint: "free up space or point output.root at a larger volume",
		}
	}

	if err := checkWritable("downloads root", root).err; err != nil {
		return doctorCheck{name: "downloads root", err: err}
	}

	return doctorCheck{name: "downloads root"}
}

func checkProxy(proxyURL, source string, ignoreEnv bool) doctorCheck {
	if proxyURL == "" {
		return doctorCheck{name: "proxy (none configured) [" + source + "]", note: ignoredEnvNote(ignoreEnv)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	if _, err := tg.ProbeProxy(ctx, proxyURL); err != nil {
		return doctorCheck{
			name: "proxy " + proxyURL + " [" + source + "]", err: err,
			hint: "verify the proxy is reachable, or clear net.proxy / --proxy to go direct",
		}
	}

	return doctorCheck{name: "proxy " + proxyURL + " [" + source + "]"}
}

// ignoredEnvNote explains a skipped standard proxy environment variable;
// empty when ignore_env is off or no such variable is set.
func ignoredEnvNote(ignoreEnv bool) string {
	if !ignoreEnv {
		return ""
	}

	if name := config.StandardProxyEnvName(); name != "" {
		return "env proxy ignored via net.ignore_env (" + name + " is set)"
	}

	return ""
}
