# teleparse

[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Lint](https://img.shields.io/badge/golangci--lint-strict%20%200%20findings-success)](https://golangci-lint.run)
[![Tests](https://img.shields.io/badge/tests-567%20passing%20%2Drace-brightgreen)](#development)
[![MTProto](https://img.shields.io/badge/MTProto-gotd%2Ftd%20v0.161.0%20(layer%20228)-8A2BE2)](https://github.com/gotd/td)

**Telegram userbot media parser CLI.** Downloads any media — photos, videos, voice messages,
video notes (кружки), documents, stickers, GIFs, audio — from any chats your **personal accounts**
can access, filtered by ~150 combinable dimensions, paced against bans, and proxied through
SOCKS4/5, HTTP CONNECT, MTProto-proxy (`dd`/`ee` fake-TLS) or the new **WEB-proxy (tproxy v1)**.

## Contents

- [Quickstart](#quickstart)
- [The one-liner this was built for](#the-one-liner-this-was-built-for)
- [Commands](#commands)
- [Filter reference](#filter-reference)
- [Configuration](#configuration)
- [Proxies](#proxies)
- [WEB-proxy v1](#web-proxy-v1)
- [Anti-ban](#anti-ban)
- [State, resume, dedup](#state-resume-dedup)
- [Architecture](#architecture)
- [Development](#development)
- [Limitations](#limitations)

## Quickstart

```bash
# credentials from https://my.telegram.org (never committed, env only)
export TELEPARSE_API_ID=123456
export TELEPARSE_API_HASH=abcdef1234567890

teleparse auth login                     # phone → code → 2FA (session in ~/.config/teleparse)
teleparse auth login --account spare     # second account; use --account spare|all anywhere
teleparse chats list --type private      # see what's accessible
teleparse scan all --media photo --last 7d           # dry-run: plan only
teleparse dl @durov --media video --min-size 5MB     # real download
teleparse sync all                       # incremental, watermark-based
```

## The one-liner this was built for

> "only zip archives under 20 MB from personal chats of people in my contacts"

```bash
teleparse dl --chat-type private --sender-contacts --media document \
  --mime application/zip --max-size 20MB --explain
```

`--explain` prints exactly which filters are pushed down to Telegram's servers
(`InputMessagesFilter*`, date bounds) and which run client-side — plus known
server quirks that apply to your combination.

## Commands

| Command | Purpose |
|---|---|
| `auth login \| logout \| status \| list` | multi-account sessions (0600, flock, stable device identity) |
| `auth login --import-telethon session.sqlite` | migrate a Telethon session's auth key |
| `chats list \| show` | dialogs with types/usernames/protected flags |
| `scan [CHATS] [FILTERS]` | `dl --dry-run`: writes manifest, downloads nothing |
| `dl [CHATS] [FILTERS]` | download; `--account a,b\|all`, `--takeout`, `--count-only` |
| `sync [CHATS]` | incremental via per-chat watermarks (cron-friendly) |
| `resume [RUN_ID]` | resume runs parked by FloodWait or interrupted |
| `runs list \| show \| clean` | run history |
| `profile save \| list \| show \| rm` | named filter presets in config |
| `export jsonl \| csv` | manifest export |
| `stats` | per-chat totals |
| `proxy show \| test` | proxy config and DC probe |
| `doctor` | config/creds/disk/DB/proxy diagnostics |

Chat specs: `all` · `@username` · `t.me/...` link · numeric id · `saved` · glob (`"News*"`).

## Filter reference

Families (AND-composed; every flag has a config-key twin):

| Family | Flags (examples) |
|---|---|
| Media type | `--media photo,video,video-note,voice,audio,document,sticker,gif` · `--sticker-kind animated` · `--has-media=false` · `--in-album only` |
| File metadata | `--mime application/zip` / `video/*` · `--ext .zip` · `--name '*.zip'` · `--name-regex` · `--min/max-size 20MB` (**before** download) · `--min/max-duration 30s` · `--min/max-width/height` · `--min-mp 2` · `--streamable` · `--video-nosound` |
| Text & entities | `--text-regex` · `--has-text only\|none` · `--hashtag news` · `--any-hashtag` · `--text-mention @user` · `--was-mentioned` (notification ≠ text!) · `--has-url` · `--url-regex` · `--has-email` · `--has-phone` · `--command /start` · `--emoji-only` |
| Dates | `--from-date 2026-01-01\|7d` · `--until-date` · `--last 7d` · `--older-than 30d` · `--edited` |
| Forwards | `--forwarded` · `--fwd-from @channel` · `--fwd-hidden` · `--fwd-date-from/to` |
| Engagement | `--is-reply` · `--min-views` · `--min-forwards` · `--min-reactions` · `--reaction 🔥` · `--pinned` |
| Chat | `--chat-type private,group,supergroup,channel,forum` · `--chat-glob 'News*'` · `--chat-regex` · `--archived only` · `--saved` · `--chat-username` · `--skip-protected` |
| Sender | `--sender-contacts` · `--sender-mutual` · `--from-me` · `--from @user,123` · `--exclude` · `--sender-bot/premium/verified/scam/deleted` · `--sender-name-regex` |
| IDs & misc | `--min-id/--max-id` · `--service only` · `--silent` · `--spoiler` |
| Execution | `--limit` (scan budget) · `--reverse` · `--dedupe unique-id\|hash\|off` · `--skip-existing` · `--recurse-topics` · `--follow-replies N` · `--albums expand\|first\|skip` |

Sizes: `500`, `10KB`, `20MB`, `1.5GiB`; durations `30s`/`10m`; dates ISO-8601 or relative `7d`/`12h`/`2w`.

## Configuration

`~/.config/teleparse/config.toml` (auto-created; unknown keys **rejected**).
Precedence: `flags > env (TELEPARSE_*) > profile > file > defaults`.

```toml
[auth]
account = "default"

[net]
proxy  = ""          # socks5:// socks4:// http:// mtproto:// webproxy://
takeout = false

[pacing]
concurrency          = 3     # parallel downloads
delay_min, delay_max = 1.0, 4.0  # jittered pause between file starts (s)
flood_sleep_threshold = 60   # ≤: auto-sleep, >: park run for `resume`
requests_per_minute  = 0     # optional global token bucket
retry_max            = 4

[output]
root      = ""                                # "" = ~/.local/share/teleparse/downloads
template  = "{chat}/{date:%Y-%m}/{filename}"
collision = "index"                           # index | overwrite | skip
sidecar   = true                              # <file>.json message metadata

[filters]           # defaults; profiles overlay these
dedupe = "unique-id"

[profiles.videos]
media = ["video"]
min_size = "5MB"
```

## Proxies

| Scheme | Transport |
|---|---|
| `socks5://user:pass@host:port` | SOCKS5 (auth supported) |
| `socks4://host:port` | SOCKS4 (in-house dialer) |
| `http://user:pass@host:port` | HTTP CONNECT (in-house dialer) |
| `mtproto://host:port/<hex-secret>` | MTProto proxy; `dd`-padded and `ee` fake-TLS secrets both supported |
| `webproxy://host:443/<secret>?carrier=websocket` | **WEB-proxy v1** (see below) |

Rotate by changing `--proxy`/config and reconnecting — the session survives; **FloodWait does not**
(it is bound to the account, not the IP). `teleparse proxy test` probes each scheme against a live DC.

## WEB-proxy v1

First client implementation of Telegram's new web-proxy protocol
([tproxy-server](https://github.com/telegramdesktop/tproxy-server), Aug 2026): MTProto frames
multiplexed (OPEN/DATA/WINDOW/CLOSE with credit-based flow control) over an HTTPS or WebSocket
carrier that looks like ordinary browsing. Bootstrap: HMAC-SHA256 capability → bridge page →
session token. Carriers: `websocket` / `websocket-lanes` (primary), `https` / `https-lanes`
(fallback). Verified against the reference `tproxy-server` in integration tests
(`-tags webproxy_integration`).

## Anti-ban

- Conservative defaults (3 parallel, 1–4 s jitter); token-bucket RPM optional.
- `FloodWait ≤ threshold` → auto-sleep; longer → run **parked** with `resume_at`,
  `teleparse resume` continues later.
- `--takeout` wraps the session in Telegram's takeout mode — the sanctioned export path with
  lower flood limits.
- Stable per-account device fingerprint (derived once, persisted, never drifts).
- Sessions 0600, single-process flock, entity cache persisted (deleted channels: the server no
  longer returns history — your local manifest is the recovery).

## State, resume, dedup

SQLite (WAL) at `~/.local/share/teleparse/state.db`, separate from sessions: per-chat
watermarks (`sync`), per-media rows with statuses/attempts/`bytes_done`, runs with park/resume.
Downloads write `<file>.part` and complete via atomic rename; re-runs are idempotent
(PK chat+message+index). Global dedup by stable Telegram file identity
(`UNIQUE(media_class, media_id)`); optional SHA-256.

## Architecture

```mermaid
flowchart LR
    CLI[cli: cobra tree] --> CFG[config: TOML+env+profiles]
    CLI --> TG[tg: accounts, login, proxy dialers]
    TG <--> GOTD[gotd/td MTProto]
    CLI --> SCAN[scan: scope, walker, mapping]
    SCAN -->|filters.Context| FILTERS[filters: ~90 predicate families]
    FILTERS -->|Pushdown: InputMessagesFilter*| GOTD
    SCAN --> STORE[(store: SQLite WAL)]
    STORE --> DL[download: pool, .part resume, hooks]
    DL -->|FloodWait| PACE[pace: jitter, park]
    DL --> FS[(files + sidecars)]
    TG -. webproxy:// .-> WP[webproxy: tproxy v1 carrier] -.-> GOTD
```

```mermaid
sequenceDiagram
    participant C as teleparse
    participant B as Bridge host
    participant R as tproxy relay
    participant T as Telegram DC
    C->>B: GET /?bridge=HMAC-capability
    B-->>C: bootstrap token (bridge page)
    C->>R: POST /api/v1/session (HELLO)
    R-->>C: WELCOME + carrier mode
    C->>R: WS tproxy-v1.<token> / HTTPS lanes
    C->>R: OPEN stream → DATA (MTProto/obfuscated2)
    R->>T: plain TCP to DC
    T-->>R: responses
    R-->>C: DATA ← stream
```

## Development

```bash
go build ./...
go vet ./...
golangci-lint run ./...   # strict: 70+ linters, 0 findings required
go test -race -count=1 ./...      # 567 tests, 11 packages
govulncheck ./...
go test -race -tags webproxy_integration ./internal/webproxy/...  # needs docker/git
CGO_ENABLED=0 go build -ldflags "-X teleparse/internal/cli.version=$(git describe --tags)" -o teleparse ./cmd/teleparse
```

## Limitations

- Deleted/private channels: once the server answers `CHANNEL_PRIVATE` no client can re-fetch
  history — the local manifest and entity cache are the only recovery path.
- Bandwidth-level download resume: gotd's public downloader re-transfers skipped ranges
  (content-safe, not byte-optimal).
- `--albums first` passes all album members per-message (coalescing happens at scan level).
- Bandwidth-level `--or` predicate groups are not implemented; composition is AND (v1).
- WEB-proxy is a frozen-v1 PoC protocol; expect drift — integration tests guard what's shipped.
