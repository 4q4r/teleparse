// Package config defines teleparse configuration: file (TOML), environment
// overrides, named filter profiles and XDG paths. Precedence is strict:
// flags > env > profile > file > defaults. One source of truth, no fallback chains.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors wrapped by dynamic validation messages.
var (
	ErrBadCollision      = errors.New("not one of index|overwrite|skip")
	ErrBadConcurrency    = errors.New("must be >= 1")
	ErrBadDelay          = errors.New("need 0 <= min <= max")
	ErrBadRetryMax       = errors.New("must be >= 1")
	ErrBadProxyURL       = errors.New("missing scheme")
	ErrBadEnvProxyScheme = errors.New("unsupported proxy scheme")
	ErrProfileAbsent     = errors.New("profile not found")
	ErrBadTildeRoot      = errors.New("cannot resolve ~")
	ErrNoHomeDir         = errors.New("cannot resolve home directory")
	ErrBadThreads        = errors.New("must be within 1..16")
	ErrBadConnections    = errors.New("must be within 1..8")
)

// Defaults for pacing (see research: conservative anti-ban numbers) and
// download speed (official clients use 2-3 connections per DC, TDLib 2).
// DefaultThreads and DefaultConnections are exported so callers can detect
// "left at the defaults" for the premium boost.
const (
	defaultConcurrency       = 3
	defaultDelayMinSeconds   = 1.0
	defaultDelayMaxSeconds   = 4.0
	defaultFloodThresholdSec = 60
	defaultRetryMax          = 4
	// DefaultThreads is the built-in per-file ranged-thread count.
	DefaultThreads = 4
	// DefaultConnections is the built-in per-DC pooled connection count.
	DefaultConnections = 3
	dirPerm            = 0o700
	filePerm           = 0o600
)

// Hard validation bounds for [download]; the ranges mirror the research
// consensus: threads beyond 16 or connections beyond 8 per DC only invite
// FLOOD_PREMIUM_WAIT without adding throughput (community turbo tops out at
// threads 8 / connections 6-8; never exceed ~20 connections per DC).
// Premium accounts may use 8 connections per DC (TDLib premium default).
const (
	threadsMin               = 1
	threadsMax               = 16
	connectionsMin           = 1
	connectionsMax           = 8
	premiumPresetThreads     = 8
	premiumPresetConnections = 8
)

// Paths holds the XDG-style locations used by teleparse, all overridable.
type Paths struct {
	ConfigDir   string // ~/.config/teleparse
	AccountsDir string // ~/.config/teleparse/accounts
	DataDir     string // ~/.local/share/teleparse
	StateDB     string // DataDir/state.db
	Downloads   string // Output.Root resolved
}

// Auth selects the default named account.
type Auth struct {
	Account string `toml:"account"`
}

// Net controls connection transport: proxy URL and takeout mode.
type Net struct {
	Proxy string `toml:"proxy"` // socks5:// socks4:// http:// mtproto:// webproxy:// or "" (direct)
	// IgnoreEnv skips automatic pickup of standard proxy environment
	// variables (HTTPS_PROXY, https_proxy, ALL_PROXY, all_proxy).
	// TELEPARSE_PROXY is always honored as an explicit app override.
	IgnoreEnv bool `toml:"ignore_env"`
	Takeout   bool `toml:"takeout"`
	// ProxySource reports where the effective proxy came from:
	// "flag", "env:NAME", "config" or "" (direct). Never read from TOML.
	ProxySource string `toml:"-"`
}

// Pacing tunes anti-ban behavior. FloodWait is account-bound: proxy rotation never clears it.
type Pacing struct {
	Concurrency         int     `toml:"concurrency"`           // parallel downloads
	DelayMin            float64 `toml:"delay_min"`             // seconds, jittered lower bound between file starts
	DelayMax            float64 `toml:"delay_max"`             // seconds, jittered upper bound
	FloodSleepThreshold int     `toml:"flood_sleep_threshold"` // auto-sleep up to N s; longer waits park the run
	RequestsPerMinute   int     `toml:"requests_per_minute"`   // global token bucket; 0 disables
	RetryMax            int     `toml:"retry_max"`             // attempts per file (exponential backoff)
}

// Output controls where and how files land on disk.
type Output struct {
	Root       string `toml:"root"`        // "" = XDG data dir
	Template   string `toml:"template"`    // path template: {chat} {date} {sender} {filename}
	Collision  string `toml:"collision"`   // index | overwrite | skip
	Sidecar    bool   `toml:"sidecar"`     // write <file>.json message metadata
	PartSuffix string `toml:"part_suffix"` // in-progress suffix
	Sha256     bool   `toml:"sha256"`      // hash after download
}

// Hooks run after each successful download. Templates may use {path}.
type Hooks struct {
	PostDownload []string `toml:"post_download"`
}

// Download tunes transfer speed: per-file ranged threads and the connection
// pool size opened per data center. Threads multiply within one file's
// transfer; connections are shared by every download routed to that DC.
type Download struct {
	Threads     int `toml:"threads"`     // ranged parts fetched in parallel per file
	Connections int `toml:"connections"` // pooled MTProto connections per DC
	// PremiumBoost upgrades the built-in default sizing to the premium
	// preset (threads 8 / connections 8) when the account is Telegram
	// Premium and neither knob was customized in the config file.
	PremiumBoost bool `toml:"premium_boost"`
}

// Effective resolves the per-run sizing pair: with the boost enabled, a
// premium account and both knobs still at the built-in defaults, the
// premium preset (8/8, the hard cap TDLib premium uses per DC) replaces
// them. Any custom threads or connections always wins verbatim.
func (d Download) Effective(premium bool) (int, int) {
	if d.PremiumBoost && premium && d.usingDefaultSizing() {
		return premiumPresetThreads, premiumPresetConnections
	}

	return d.Threads, d.Connections
}

// usingDefaultSizing reports whether the config left both download knobs at
// the built-in defaults, the signal the premium boost may retune them.
func (d Download) usingDefaultSizing() bool {
	return d.Threads == DefaultThreads && d.Connections == DefaultConnections
}

// Config is the root configuration object.
type Config struct {
	Auth     Auth               `toml:"auth"`
	Net      Net                `toml:"net"`
	Pacing   Pacing             `toml:"pacing"`
	Output   Output             `toml:"output"`
	Download Download           `toml:"download"`
	Filters  Filters            `toml:"filters"`
	Hooks    Hooks              `toml:"hooks"`
	Profiles map[string]Filters `toml:"profiles"`
}

// Load reads the TOML config at path (created as a fully commented template
// if missing), applies environment overrides (TELEPARSE_* and the standard
// proxy variables) and returns the merged config with paths.
func Load(path string) (*Config, *Paths, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	cfg := Default()

	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("read config %s: %w", path, err)
		}

		if err := writeTemplate(path); err != nil {
			return nil, nil, fmt.Errorf("create default config %s: %w", path, err)
		}
	} else if err := decodeTOML(string(raw), cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if cfg.Net.Proxy != "" {
		cfg.Net.ProxySource = "config"
	}

	if err := applyEnv(cfg); err != nil {
		return nil, nil, fmt.Errorf("invalid env: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	paths, err := buildPaths(cfg)
	if err != nil {
		return nil, nil, err
	}

	return cfg, paths, nil
}

// Default returns built-in defaults.
func Default() *Config {
	return &Config{
		Auth: Auth{Account: "default"},
		Net:  Net{},
		Pacing: Pacing{
			Concurrency:         defaultConcurrency,
			DelayMin:            defaultDelayMinSeconds,
			DelayMax:            defaultDelayMaxSeconds,
			FloodSleepThreshold: defaultFloodThresholdSec,
			RequestsPerMinute:   0,
			RetryMax:            defaultRetryMax,
		},
		Output: Output{
			Root:       "",
			Template:   "{chat}/{date:%Y-%m}/{filename}",
			Collision:  "index",
			Sidecar:    true,
			PartSuffix: ".part",
			Sha256:     false,
		},
		Download: Download{
			Threads:      DefaultThreads,
			Connections:  DefaultConnections,
			PremiumBoost: true,
		},
		Filters: Filters{
			Dedupe: "unique-id",
			Recursion: Recursion{
				Topics: true,
				Albums: "expand",
			},
		},
		Profiles: map[string]Filters{},
	}
}

// Validate checks semantic constraints.
func (c *Config) Validate() error {
	switch c.Output.Collision {
	case "", "index", "overwrite", "skip":
	default:
		return fmt.Errorf("output.collision %q: %w", c.Output.Collision, ErrBadCollision)
	}

	if err := c.validatePacing(); err != nil {
		return err
	}

	if err := c.validateDownload(); err != nil {
		return err
	}

	if c.Net.Proxy != "" && !strings.Contains(c.Net.Proxy, "://") {
		return fmt.Errorf("net.proxy %q: %w (socks5:// socks4:// http:// mtproto:// webproxy://)",
			c.Net.Proxy, ErrBadProxyURL)
	}

	for name, prof := range c.Profiles {
		if err := prof.Validate(); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}

	if err := c.Filters.Validate(); err != nil {
		return fmt.Errorf("filters: %w", err)
	}

	return nil
}

// Profile returns the named filter profile merged over current defaults.
func (c *Config) Profile(name string) (*Filters, error) {
	prof, ok := c.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("%q: %w", name, ErrProfileAbsent)
	}

	var out Filters

	fillProfile(&out, &c.Filters, &prof)

	return &out, nil
}

func (c *Config) validatePacing() error {
	if c.Pacing.Concurrency < 1 {
		return fmt.Errorf("pacing.concurrency %d: %w", c.Pacing.Concurrency, ErrBadConcurrency)
	}

	if c.Pacing.DelayMin < 0 || c.Pacing.DelayMax < c.Pacing.DelayMin {
		return fmt.Errorf("pacing.delay [%v, %v]: %w", c.Pacing.DelayMin, c.Pacing.DelayMax, ErrBadDelay)
	}

	if c.Pacing.RetryMax < 1 {
		return fmt.Errorf("pacing.retry_max %d: %w", c.Pacing.RetryMax, ErrBadRetryMax)
	}

	return nil
}

func (c *Config) validateDownload() error {
	if c.Download.Threads < threadsMin || c.Download.Threads > threadsMax {
		return fmt.Errorf("download.threads %d: %w", c.Download.Threads, ErrBadThreads)
	}

	if c.Download.Connections < connectionsMin || c.Download.Connections > connectionsMax {
		return fmt.Errorf("download.connections %d: %w", c.Download.Connections, ErrBadConnections)
	}

	return nil
}

// DefaultConfigPath returns ~/.config/teleparse/config.toml (respects XDG_CONFIG_HOME).
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}

	return filepath.Join(dir, "teleparse", "config.toml")
}

func buildPaths(cfg *Config) (*Paths, error) {
	configDir := filepath.Dir(DefaultConfigPath())

	dataDir, err := userDataDir()
	if err != nil {
		return nil, err
	}

	root, err := resolveRoot(cfg.Output.Root, dataDir)
	if err != nil {
		return nil, err
	}

	return &Paths{
		ConfigDir:   configDir,
		AccountsDir: filepath.Join(configDir, "accounts"),
		DataDir:     dataDir,
		StateDB:     filepath.Join(dataDir, "state.db"),
		Downloads:   root,
	}, nil
}

func resolveRoot(root, dataDir string) (string, error) {
	switch {
	case root == "":
		return filepath.Join(dataDir, "downloads"), nil
	case strings.HasPrefix(root, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("output.root %q: %w: %w", root, ErrBadTildeRoot, err)
		}

		return filepath.Join(home, root[2:]), nil
	default:
		return root, nil
	}
}

func userDataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "teleparse"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve data dir: %w: %w", ErrNoHomeDir, err)
	}

	return filepath.Join(home, ".local", "share", "teleparse"), nil
}
