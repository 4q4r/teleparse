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

# Route the session through Telegram takeout (data export) endpoints:
# slower, but with gentler rate limits.
# takeout = false
# Auto-enable takeout (export) mode when a scan looks large: lower flood
# limits for the whole run; the export session finishes when the run ends.
# Disable with false; --no-takeout overrides per invocation, --takeout forces.
# takeout_auto = true
# Estimated scope size (chats) that triggers auto takeout.
# takeout_auto_min_chats = 50
` + templateTail()
}

// templateTail holds the pacing/output/download/hooks sections.
func templateTail() string {
	return `# [pacing] tunes anti-ban behavior for downloads.

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
# Accepted for compatibility; downloads are sequential ranged streams per
# file - throughput scales with connections and pacing concurrency.

# connections = 3
# MTProto connections pooled per data center, shared by all files routed
# to that DC (1-8). Each chunk request takes an idle pooled connection.
# Turbo preset for impatient lines:
#   connections = 6
# Never exceed ~20 total connections per DC: beyond that Telegram answers
# FLOOD_PREMIUM_WAIT (an account-level throttle; Telegram Premium removes
# it). Short waits are slept automatically and surfaced in the live UI as
# "throttled Ns" - they do not park the run.

# premium_boost = true
# Premium accounts are auto-detected (cached 24h) and upgrade the default
# connection sizing to the premium preset (8 connections) - TDLib's
# premium envelope. Explicit connections always win; false pins the
# defaults.

# [scan] tunes walk caching for scan/dl/sync.

[scan]
# incremental = true
# Reuse per-chat watermarks: repeat runs walk only messages newer than the
# last successful pass instead of re-walking full history (10+ minute runs
# become seconds). History-clear detection resets a chat whose newest
# message dropped below its watermark. Pass --full to any of dl/scan/sync
# to force one complete re-walk; set false here to always walk in full.

# rewalk_min_age = "10m"
# Per-chat walk freshness: a chat walked less than this ago is not walked
# again at all - a run restarted seconds after a stop settles each
# recently-walked chat instantly from the cached manifest (its pending
# downloads are still served) instead of re-probing every chat. Relative
# duration (30s, 10m, 1h); "0" walks every chat on every run. --full
# ignores freshness along with watermarks.

# [output] controls where and how downloaded files land.

[output]
# root = ""
# Downloads root. Empty (default) uses the XDG data dir
# (~/.local/share/teleparse/downloads); a leading ~/ is expanded.
# With filters.dedupe = "hardlink" (the default) the hidden blob store
# lives under <root>/.teleparse/blobs: keep the root on one filesystem
# so chat copies can hardlink to their blobs.

# template = "{chat}/{date:%Y-%m}/{filename}"
# Path template under root. Placeholders: {chat} {sender} {date:YYYY-MM}
# {filename} {msgid} {ext}.

# naming = "original"
# How {filename} renders: original keeps Telegram's own file name;
# msgid names files <messageID>_<index><ext> (collision-proof) while the
# directories stay template-driven. Applies to fresh and cached downloads
# alike.

# collision = "index"
# What to do when the target path exists: index renames to name-1.ext,
# overwrite replaces it silently, skip keeps the existing file.

# metadata = "chat"
# Download metadata output: chat keeps ONE manifest.json per chat
# directory (files numbered seq 1..N in completion order, paths relative
# to the manifest); file writes a legacy <file>.json sidecar next to each
# download; off writes nothing.
# The legacy sidecar = true/false still maps to metadata = "file"/"off"
# when metadata is not set.

# sidecar = true
# Deprecated: use metadata above. Only consulted when metadata is unset.

# part_suffix = ".part"
# Suffix for in-progress downloads, renamed away on completion.

# sha256 = false
# Hash each file after download (stored in the state DB).

# rewrite_ext = false
# Rewrite a downloaded file's on-disk extension to the canonical one for
# the mime type Telegram recorded (a .rar-named upload that is really
# application/zip lands as .zip) when they disagree. Matching (or
# equivalent, e.g. .jpg/.jpeg) extensions, unknown mime types and compound
# names such as .tar.gz are never touched; the manifest keeps the original
# Telegram file name. --rewrite-ext enables it per run.
` + templateRunHooksTail()
}

// templateRunHooksTail holds the run-notification and hooks sections.
func templateRunHooksTail() string {
	return `# [run] tunes per-run notifications.

[run]
# notify_webhook = ""
# POST a JSON run summary to this URL when a dl/sync/get run ends and when
# a flood wait parks it (payload: counters, duration, top failure reasons;
# 5s timeout, one retry, failures never fail the run). Also set via
# TELEPARSE_WEBHOOK; --notify-webhook wins per invocation; empty disables.

# [hooks] runs shell commands around downloads.

[hooks]
# post_download = ["notify-send 'saved {path}'"]
# Commands run in order after each successful download; {path} expands
# to the finished file path.
`
}
