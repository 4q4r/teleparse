package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeTemplate writes the fully commented default configuration template
// to path with 0600, creating parent dirs. Only first-run creation uses
// it; Config.Save keeps marshaling live values.
func writeTemplate(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("%w: %w", ErrMakeConfigDir, err)
	}

	if err := os.WriteFile(path, []byte(defaultTemplate()), filePerm); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	return nil
}

// defaultTemplate renders the commented configuration template written on
// first run. Every option ships commented out so the built-in defaults
// stay active; per-run settings ([filters], [profiles]) are deliberately
// absent because they belong to flags and the profile command.
func defaultTemplate() string {
	return `# teleparse configuration
#
# Generated on first run with every option commented out: uncomment a
# line to override the built-in default. Delete this file to regenerate.
#
# Precedence, highest first:
#   1. command-line flags (--proxy, --account, ...)
#   2. environment: TELEPARSE_PROXY, then the standard HTTPS_PROXY /
#      https_proxy / ALL_PROXY / all_proxy (unless net.ignore_env = true)
#   3. this file
#
# Per-run filter settings live in flags and named profiles
# ("teleparse profile save NAME"), not in this file.

# [auth] picks the named account (session) used by commands.

[auth]
# account = "default"
# Session name under ~/.config/teleparse/accounts/.
# Create sessions with: teleparse auth login --account NAME

# [net] controls the connection transport to Telegram.

[net]
# proxy = "socks5://127.0.0.1:1080"
# Proxy for all Telegram traffic; empty (default) connects directly.
# Allowed schemes:
#   socks5://[user:pass@]host:port
#   socks4://host:port
#   http://[user:pass@]host:port          (HTTP CONNECT)
#   mtproto://host:port/HEX_SECRET        (MTProxy)
#   webproxy://host:443/SECRET?carrier=websocket
# carrier accepts auto | websocket | https.
# The same setting is picked up automatically from HTTPS_PROXY or
# ALL_PROXY; see ignore_env below.

# net.ignore_env = false
# When true, skip automatic pickup of HTTPS_PROXY, https_proxy, ALL_PROXY
# and all_proxy - useful when a system-wide HTTPS_PROXY must not apply to
# teleparse. TELEPARSE_PROXY is always honored regardless.

# takeout = false
# Route the session through Telegram takeout (data export) endpoints:
# slower, but with gentler rate limits.

# [pacing] tunes anti-ban behavior for downloads.

[pacing]
# concurrency = 3
# Parallel file downloads. Keep low (1-5) to stay unremarkable.

# delay_min = 1.0
# delay_max = 4.0
# Jittered pause in seconds between file starts; a random value from
# [delay_min, delay_max] is drawn each time. Fractions are allowed.

# flood_sleep_threshold = 60
# Auto-sleep on FLOOD_WAIT up to this many seconds; longer waits park
# the run for "teleparse resume" instead of sleeping. Flood waits bind
# to the account: proxy rotation does not clear them.

# requests_per_minute = 0
# Global rate limit via a token bucket; 0 (default) disables the limiter.

# retry_max = 4
# Attempts per failed file with exponential backoff; must be >= 1.

# [download] tunes transfer speed. Defaults mirror official clients
# (TDLib uses 2 download connections per DC; 3 is safely unremarkable).

[download]
# threads = 4
# Ranged parts fetched in parallel per file (1-16). Each thread rides its
# own pooled connection, so one large file can saturate a link.

# connections = 3
# MTProto connections pooled per data center, shared by all files routed
# to that DC (1-8). Turbo preset for impatient lines:
#   threads = 8, connections = 6
# Never exceed ~20 total connections per DC: beyond that Telegram answers
# FLOOD_PREMIUM_WAIT (an account-level throttle; Telegram Premium removes
# it). Short waits are slept automatically and surfaced in the live UI as
# "throttled Ns" - they do not park the run.

# premium_boost = true
# Premium accounts are auto-detected (cached 24h) and upgrade the default
# sizing to the premium preset (8/8) - TDLib's premium envelope. Explicit
# threads/connections always win; false pins the defaults.

# [output] controls where and how downloaded files land.

[output]
# root = ""
# Downloads root. Empty (default) uses the XDG data dir
# (~/.local/share/teleparse/downloads); a leading ~/ is expanded.

# template = "{chat}/{date:%Y-%m}/{filename}"
# Path template under root. Placeholders: {chat} {sender} {date:YYYY-MM}
# {filename} {msgid} {ext}.

# collision = "index"
# What to do when the target path exists: index renames to name-1.ext,
# overwrite replaces it silently, skip keeps the existing file.

# sidecar = true
# Write a <file>.json sidecar with message metadata next to each download.

# part_suffix = ".part"
# Suffix for in-progress downloads, renamed away on completion.

# sha256 = false
# Hash each file after download (stored in the state DB).

# [hooks] runs shell commands around downloads.

[hooks]
# post_download = ["notify-send 'saved {path}'"]
# Commands run in order after each successful download; {path} expands
# to the finished file path.
`
}
