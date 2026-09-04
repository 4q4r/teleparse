package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

// Sentinel errors wrapped by dynamic doctor messages.
var (
	errDoctorFailed = errors.New("one or more doctor checks failed")
	errLowDiskSpace = errors.New("less than 100 MiB free under the downloads root")
)

// doctorCheck is one PASS/FAIL line.
type doctorCheck struct {
	name string
	err  error
}

func doctorCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, API credentials, session, disk, database",
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := []func() doctorCheck{
				checkAPICreds,
				func() doctorCheck { return checkWritable("accounts dir", app.paths.AccountsDir) },
				func() doctorCheck { return checkWritable("state db dir", filepath.Dir(app.paths.StateDB)) },
				func() doctorCheck { return checkDownloads(app.paths.Downloads) },
				func() doctorCheck { return checkProxy(app.cfg.Net.Proxy) },
			}

			failed := 0

			for _, check := range checks {
				result := check()
				if result.err != nil {
					failed++

					if err := printLine(cmd, "FAIL %s: %v\n", result.name, result.err); err != nil {
						return fail(cmd, err)
					}

					continue
				}

				if err := printLine(cmd, "PASS %s\n", result.name); err != nil {
					return fail(cmd, err)
				}
			}

			if failed > 0 {
				return fmt.Errorf("%d: %w", failed, errDoctorFailed)
			}

			return nil
		},
	}
}

func checkAPICreds() doctorCheck {
	if _, _, err := tg.CredsFromEnv(); err != nil {
		return doctorCheck{name: "api credentials", err: err}
	}

	return doctorCheck{name: "api credentials"}
}

func checkWritable(name, dir string) doctorCheck {
	if err := os.MkdirAll(dir, doctorDirPerm); err != nil {
		return doctorCheck{name: name, err: fmt.Errorf("create %s: %w", dir, err)}
	}

	probe := filepath.Join(dir, ".teleparse-doctor")

	if err := os.WriteFile(probe, nil, doctorFilePerm); err != nil {
		return doctorCheck{name: name, err: fmt.Errorf("write %s: %w", probe, err)}
	}

	if err := os.Remove(probe); err != nil {
		return doctorCheck{name: name, err: fmt.Errorf("clean %s: %w", probe, err)}
	}

	return doctorCheck{name: name}
}

func checkDownloads(root string) doctorCheck {
	if err := os.MkdirAll(root, doctorDirPerm); err != nil {
		return doctorCheck{name: "downloads root", err: fmt.Errorf("create %s: %w", root, err)}
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
		return doctorCheck{name: "downloads root", err: fmt.Errorf("%d bytes: %w", free, errLowDiskSpace)}
	}

	if err := checkWritable("downloads root", root).err; err != nil {
		return doctorCheck{name: "downloads root", err: err}
	}

	return doctorCheck{name: "downloads root"}
}

func checkProxy(proxyURL string) doctorCheck {
	if proxyURL == "" {
		return doctorCheck{name: "proxy (none configured)"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	if _, err := tg.ProbeProxy(ctx, proxyURL); err != nil {
		return doctorCheck{name: "proxy " + proxyURL, err: err}
	}

	return doctorCheck{name: "proxy " + proxyURL}
}
