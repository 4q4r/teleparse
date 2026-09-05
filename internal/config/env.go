package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// EnvVarPrefix is the environment override namespace.
const EnvVarPrefix = "TELEPARSE"

// applyEnv overlays TELEPARSE_* environment variables onto cfg and picks up
// the standard proxy environment when no explicit override is set.
// Supported: TELEPARSE_ACCOUNT, TELEPARSE_PROXY, TELEPARSE_TAKEOUT,
// TELEPARSE_CONCURRENCY, TELEPARSE_ROOT, TELEPARSE_PROFILE (profile name applied by caller).
// API credentials are resolved separately (see credentials.go): environment
// first, then the credentials file.
func applyEnv(cfg *Config) error {
	if v := env("ACCOUNT"); v != "" {
		cfg.Auth.Account = v
	}

	if err := applyProxyEnv(cfg); err != nil {
		return err
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

	return nil
}

// applyProxyEnv resolves the effective proxy. TELEPARSE_PROXY beats the
// standard environment (HTTPS_PROXY, https_proxy, ALL_PROXY, all_proxy),
// which in turn outranks the config file — curl's precedence.
// net.ignore_env skips only the standard variables, never TELEPARSE_PROXY:
// the app-scoped variable is an explicit user override.
func applyProxyEnv(cfg *Config) error {
	if v := env("PROXY"); v != "" {
		cfg.Net.Proxy = v
		cfg.Net.ProxySource = "env:" + EnvVarPrefix + "_PROXY"

		return nil
	}

	name, raw := standardProxyEnv()
	if name == "" || cfg.Net.IgnoreEnv {
		return nil
	}

	proxy, err := normalizeEnvProxy(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	cfg.Net.Proxy = proxy
	cfg.Net.ProxySource = "env:" + name

	return nil
}

// standardProxyEnvNames lists the standard proxy environment variables
// honored, in lookup order (first non-empty wins).
func standardProxyEnvNames() []string {
	return []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"}
}

// standardProxyEnv returns the name and raw value of the first set
// standard proxy environment variable, or "" when none is set.
func standardProxyEnv() (string, string) {
	for _, name := range standardProxyEnvNames() {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return name, v
		}
	}

	return "", ""
}

// StandardProxyEnvName returns the name of the first set standard proxy
// environment variable (HTTPS_PROXY, https_proxy, ALL_PROXY, all_proxy),
// or "" when none is set. It reports set variables regardless of
// net.ignore_env, so callers can explain when a variable is being skipped.
func StandardProxyEnvName() string {
	name, _ := standardProxyEnv()

	return name
}

// normalizeEnvProxy turns a raw environment value into a usable proxy URL.
// Scheme-less values are read as http:// (the Go convention); the scheme
// must then be one teleparse supports.
func normalizeEnvProxy(raw string) (string, error) {
	value := raw
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}

	scheme, _, _ := strings.Cut(value, "://")

	switch strings.ToLower(scheme) {
	case "socks5", "socks4", "http", "mtproto", "webproxy":
		return value, nil
	default:
		return "", fmt.Errorf("%q: %w (allowed: socks5:// socks4:// http:// mtproto:// webproxy://; "+
			"set net.ignore_env = true in config.toml to skip env proxy pickup)", value, ErrBadEnvProxyScheme)
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
