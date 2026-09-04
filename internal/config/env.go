package config

import (
	"os"
	"strconv"
	"strings"
)

// EnvVarPrefix is the environment override namespace.
const EnvVarPrefix = "TELEPARSE"

// applyEnv overlays TELEPARSE_* environment variables onto cfg.
// Supported: TELEPARSE_ACCOUNT, TELEPARSE_PROXY, TELEPARSE_TAKEOUT,
// TELEPARSE_CONCURRENCY, TELEPARSE_ROOT, TELEPARSE_PROFILE (profile name applied by caller).
// API credentials never live in the config file: TELEPARSE_API_ID / TELEPARSE_API_HASH.
func applyEnv(cfg *Config) {
	if v := env("ACCOUNT"); v != "" {
		cfg.Auth.Account = v
	}

	if v := env("PROXY"); v != "" {
		cfg.Net.Proxy = v
	}

	if v, ok := envBool("TAKEOUT"); ok {
		cfg.Net.Takeout = v
	}

	if v := env("CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Pacing.Concurrency = n
		}
	}

	if v := env("ROOT"); v != "" {
		cfg.Output.Root = v
	}
}

func env(name string) string {
	return strings.TrimSpace(os.Getenv(EnvVarPrefix + "_" + name))
}

func envBool(name string) (bool, bool) {
	v := env(name)
	if v == "" {
		return false, false
	}

	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}

	return b, true
}
