# 📥 teleparse

<div align="center">

**Telegram userbot media archival engine**

Archival downloads of any media — photos, videos, video notes, voice, audio,
documents, stickers, GIFs — from every chat your personal accounts can reach,
filtered by ~150 combinable dimensions, paced against bans, and proxied
through SOCKS4/5, HTTP CONNECT, MTProto-proxy or WEB-proxy (tproxy v1).

[![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev/)
[![MTProto](https://img.shields.io/badge/gotd%2Ftd-v0.161.0%20%C2%B7%20Layer%20228-8A2BE2?style=for-the-badge)](https://github.com/gotd/td)
[![License](https://img.shields.io/badge/License-MIT-green?style=for-the-badge)](LICENSE)
[![Release](https://img.shields.io/github/v/release/4q4r/teleparse?style=for-the-badge)](https://github.com/4q4r/teleparse/releases)

[![CI](https://img.shields.io/github/actions/workflow/status/4q4r/teleparse/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white)](https://github.com/4q4r/teleparse/actions/workflows/ci.yml)
[![Last commit](https://img.shields.io/github/last-commit/4q4r/teleparse?style=flat-square&logo=github&logoColor=white)](https://github.com/4q4r/teleparse/commits/main)
[![Repo size](https://img.shields.io/github/repo-size/4q4r/teleparse?style=flat-square&logo=github&logoColor=white)](https://github.com/4q4r/teleparse)
[![Tests](https://img.shields.io/badge/tests-1357%20passing%20%2Drace-brightgreen?style=flat-square)](#-testing)
[![Lint](https://img.shields.io/badge/golangci--lint-strict%2C%200%20findings-success?style=flat-square)](https://golangci-lint.run)

[Architecture](#-system-architecture) · [Quick Start](#-quick-start) · [Commands](#-command-surface) · [Configuration](#-configuration) · [Security](#-security) · [Testing](#-testing)

</div>

---

## 📑 Table of Contents

- [System Architecture](#-system-architecture)
- [Project Structure](#-project-structure)
- [Core Modules](#-core-modules)
- [Filters](#-filters)
- [Downloads & Dedup](#-downloads--dedup)
- [Proxies & WEB-proxy v1](#-proxies--web-proxy-v1)
- [Anti-ban & Accounts](#-anti-ban--accounts)
- [Command Surface](#-command-surface)
- [Configuration](#-configuration)
- [Security](#-security)
- [Quick Start](#-quick-start)
- [Local Development](#-local-development)
- [Testing](#-testing)
- [Filesystem Layout](#-filesystem-layout)

---

## 🗺️ System Architecture

```mermaid
flowchart TB
    CLI[cli: cobra tree] --> CFG[config: TOML + env + profiles]
    CLI --> TG[tg: accounts, login, premium, proxy dialers]
    TG <--> GOTD[gotd/td MTProto Layer 228]

    subgraph PLAN["Scan & filter"]
      SCAN[scan: scope resolve, incremental walker]
      FILTERS[filters: ~150 dimensions]
    end

    CLI --> SCAN
    SCAN -->|filters.Context| FILTERS
    FILTERS -->|"pushdown: InputMessagesFilter*"| GOTD

    subgraph DL["Store & download"]
      STORE[(store: SQLite WAL)]
      DLN[download: ranged pool, .part resume]
      PACE[pace: jitter, FloodWait park]
    end

    SCAN --> STORE
    STORE --> DLN
    DLN -->|throttle| PACE
    DLN --> FS[(files + per-chat manifests)]

    TG -.->|"webproxy://"| WP[webproxy: tproxy v1 carrier] -.-> GOTD
```

Request flow — what one `teleparse dl` run does:

1. The CLI resolves configuration (flags > env > profile > file > defaults)
   and API credentials (`TELEPARSE_API_ID`/`TELEPARSE_API_HASH` env or
   `~/.config/teleparse/credentials.toml`).
2. The session connects through the effective proxy (or direct), detects
   Telegram Premium state (cached 24 h per account) and sizes the per-DC
   connection pool accordingly.
3. The takeout decision runs: with `takeout_auto = true` (the default), a
   scope estimated at ≥ `takeout_auto_min_chats` (default 50) chats wraps the
   session in Telegram's export mode; `--takeout` forces it, `--no-takeout`
   disables it. The export session finishes with the run.
4. Chat specs (`all` · `@username` · `t.me/...` · numeric id · `saved` ·
   glob) are resolved through the dialog cache and deduplicated by chat id,
   so overlapping specs never walk twice.
5. A per-chat freshness probe runs: the dialogs page carries each chat's
   newest message id for free; if it dropped below the cached watermark the
   chat was mass-cleared and is re-walked in full. Chats whose last
   successful walk is younger than `scan.rewalk_min_age` (default `10m`)
   settle instantly from cache.
6. The incremental walker asks Telegram only for messages past each chat's
   watermark. Filters with a server pushdown (`InputMessagesFilter*` classes,
   date bounds) shrink every page; remaining predicates evaluate client-side
   against `filters.Context`.
7. New matches merge into the per-chat manifest, together with cached pending
   rows that still owe a download (discovered, queued or failed on any
   earlier run — even with different filters).
8. The paced worker pool transfers each file as a sequential stream of ranged
   512 KiB requests over pooled per-DC MTProto connections, resuming at the
   exact on-disk `.part` offset and retrying failed chunks with exponential
   backoff. `FloodWait` ≤ `flood_sleep_threshold` auto-sleeps; longer waits
   park the run for `teleparse resume`.
9. Completed files land once in the hardlink blob store
   (`<root>/.teleparse/blobs/`) and are hardlinked into each chat's templated
   path; manifests, metadata, optional SHA-256 and `post_download` hooks run
   per file.
10. Run-end hooks fire: `--notify-webhook` posts a JSON summary (also on
    flood-wait park), and `verify`/`dedupe` can sweep the result offline.

---

## 📂 Project Structure

```text
teleparse/
├── cmd/
│   └── teleparse/                single entry point wiring the command tree
├── internal/
│   ├── cli/                      cobra commands, run orchestration, progress UI
│   ├── config/                   TOML + env + profiles, XDG paths, credentials
│   ├── tg/                       gotd gateway: sessions, login, premium, dialers
│   ├── transport/                carrier-failure classification for retries
│   ├── scan/                     scope resolution, incremental history walker
│   ├── filters/                  options model + predicate compiler (~150 dims)
│   ├── download/                 paced pipeline: templating, .part resume, hooks
│   ├── store/                    SQLite WAL: manifest, watermarks, runs
│   ├── pace/                     jitter, concurrency semaphore, flood parking
│   ├── webproxy/                 WEB-proxy v1 (tproxy) carrier client
│   ├── verify/                   offline integrity sweep + native format checks
│   ├── notify/                   run-outcome webhooks
│   ├── export/                   manifest export to JSONL / CSV
│   └── testutil/                 shared test helpers
│       └── tlmock/               in-memory Telegram API mock
├── docs/specs/                   design documents
├── .github/workflows/            CI (lint, test, integration, build) + release
├── .golangci.yml                 strict lint config (~70 linters, 0 findings)
├── .goreleaser.yml               cross-platform release pipeline
├── Makefile                      build / lint / test / check targets
├── CHANGELOG.md
└── CONTRIBUTING.md
```

---

## 🧩 Core Modules

| Module | Purpose | Key surface |
| :-- | :-- | :-- |
| `cli` | Command tree, run orchestration, live progress UI | 18 commands; 105 flags on `dl` |
| `config` | TOML + env + profile config, XDG paths, credential resolution | `config.toml`, `credentials.toml`, `TELEPARSE_*` |
| `tg` | gotd gateway: multi-account sessions, login, premium detect, proxy dialers | `auth`, dialogs, contacts |
| `transport` | Classifies carrier failures (dead proxy tunnels, truncated streams) for retry decisions | internal |
| `scan` | Resolves chat scopes, walks history incrementally, applies server pushdown | internal |
| `filters` | Single options model for flags/config/profiles; compiles to pushdown + predicates | `--explain`, ~150 dimensions |
| `download` | Paced worker pool, ranged transfer, `.part` resume, collision policy, hooks | internal |
| `store` | SQLite WAL persistence: media manifest, watermarks, run bookkeeping | `state.db` |
| `pace` | Jittered inter-download delays, concurrency semaphore, flood-wait parking | internal |
| `webproxy` | WEB-proxy v1 (tproxy) client as a gotd resolver | `webproxy://` URLs |
| `verify` | Offline manifest-vs-disk sweep, native archive validation | `verify --deep` / `--fix` |
| `notify` | Best-effort run-outcome webhooks (run end, flood park) | `--notify-webhook` |
| `export` | Renders manifest rows as JSONL or CSV | `export jsonl\|csv` |
| `testutil/tlmock` | In-memory Telegram API mock for tests | internal |

---

## 🔎 Filters

The filter engine lives in `internal/filters`: one options model drives CLI
flags, config keys and TOML profiles, and compiles into a server pushdown
plan plus client-side predicates. `dl`, `scan`, `sync` and `get` expose
**105 flags** covering ~150 combinable dimensions; families compose with AND,
and every flag has a config-key twin.

| Family | Flags (examples) |
| :-- | :-- |
| Media type | `--media photo,video,video-note,voice,audio,document,sticker,gif` · `--exclude-media` (skips listed kinds; **overrides** `--media` matches) · `--sticker-kind animated` · `--has-media=false` · `--in-album only` |
| File metadata | `--mime application/zip` / `video/*` · `--exclude-mime` · `--ext .zip` · `--exclude-ext .mp4` · `--name '*.zip'` · `--name-regex` · `--min/max-size 20MB` (**before** download) · `--min/max-duration 30s` · `--min/max-width/height` · `--min-mp 2` · `--streamable` · `--video-nosound` |
| Text & entities | `--text-regex` · `--has-text only\|none` · `--hashtag news` · `--any-hashtag` · `--text-mention @user` · `--was-mentioned` (notification ≠ text!) · `--has-url` · `--url-regex` · `--has-email` · `--has-phone` · `--command /start` · `--emoji-only` |
| Dates | `--from-date 2026-01-01\|7d` · `--until-date` · `--last 7d` · `--older-than 30d` · `--edited` |
| Forwards | `--forwarded` · `--fwd-from @channel` · `--fwd-hidden` · `--fwd-date-from/to` |
| Engagement | `--is-reply` · `--min-views` · `--min-forwards` · `--min-reactions` · `--reaction 🔥` · `--pinned` |
| Chat | `--chat-type private,group,supergroup,channel,forum` · `--exclude-chat-type` · `--chat-glob 'News*'` · `--chat-regex` · `--archived only` · `--saved` · `--chat-username` · `--chat-deleted=true` · `--skip-protected` |
| Sender | `--sender-contacts` · `--sender-non-contacts` · `--sender-mutual` / `--sender-non-mutual` · `--from-me` · `--from @user,123,+15551234567` (ids \| usernames \| phones) · `--exclude` (same syntax) · `--sender-bot/premium/verified/scam/deleted` · `--sender-name-regex` · `--sender-username-regex` · `--sender-phone-regex` |
| Chat age | `--chat-min-age 1y` — only chats with messages older than the cutoff; costs one probe RPC per chat (private chats expose no creation date) |
| IDs & misc | `--min-id/--max-id` · `--service only` · `--silent` · `--spoiler` |
| Execution | `--limit` (scan budget per chat) · `--reverse` · `--dedupe hardlink\|unique-id\|hash\|off` · `--skip-existing` · `--order date\|id` |

Sizes: `500`, `10KB`, `20MB`, `1.5GiB`; durations `30s`/`10m`; dates ISO-8601
or relative `7d`/`12h`/`2w`.

**Exclude semantics.** Every `--exclude-*` family evaluates *after* its
include twin: a file or chat matching both is dropped, and an exclude alone
(no include set) acts as pure negation — everything except the listed values
passes.

**Pushdown.** Whenever a filter combination maps onto a Telegram
`InputMessagesFilter*` class or date bounds, the walker pushes it to the
server so fewer pages and smaller pages cross the wire; everything else
evaluates client-side. `--explain` prints exactly which filters push down,
which run locally, and the known server quirks that apply to your
combination — then proceeds with the run.

The one-liner this was built for:

```bash
teleparse dl --chat-type private --sender-contacts --media document \
  --mime application/zip --max-size 20MB --explain
```

> "only zip archives under 20 MB from personal chats of people in my contacts"

---

## 📥 Downloads & Dedup

**Ranged transfer engine.** Every file downloads as a sequential stream of
ranged 512 KiB requests over a pooled set of MTProto connections per data
center (default 3, max 8). Files know their DC from the manifest; unknown DCs
start on the home pool and follow a `FILE_MIGRATE` answer to the right one.
A resumed `.part` file continues at the exact on-disk offset — no byte is
ever re-downloaded. Failed chunks retry with exponential backoff
(`pacing.retry_max`, default 4). `[download] threads` is accepted for
compatibility only; the per-file transfer is sequential and throughput scales
with `connections` and `pacing.concurrency`. Beyond ~20 connections per DC
Telegram answers `FLOOD_PREMIUM_WAIT` (an account-level throttle lifted by
Telegram Premium) — short waits auto-sleep and show as `throttled Ns` in the
live UI.

**Hardlink blob store (default dedup).** The same Telegram file sighted in N
chats downloads exactly once into
`<root>/.teleparse/blobs/<class>/<id><ext>` and is **hardlinked** into each
chat's templated path — zero extra network, zero extra disk for repeats. A
hardlink *is* the file (same inode), so deleting any subset of copies never
breaks the survivors. On filesystems without hardlink support the link
degrades to a byte copy (counted as `link_copies`). `teleparse dedupe stats`
reports blob usage; `dedupe gc` unlinks blobs whose last consumer is gone.

**Naming and metadata.** `[output]` controls where and how files land:

| Key | Values |
| :-- | :-- |
| `template` | `{chat}/{date:%Y-%m}/{filename}` — `{chat}`, `{date}`, `{sender}`, `{filename}` |
| `naming` | `original` (keep Telegram names) \| `msgid` (`<msgID>_<index><ext>`) |
| `collision` | `index` \| `overwrite` \| `skip` |
| `metadata` | `chat` (one `manifest.json` per chat dir) \| `file` (legacy per-file sidecar) \| `off` |
| `rewrite_ext` | rename mismatched extensions to the canonical one for the recorded mime type |

**Verify: offline integrity with repair.** `teleparse verify` works fully
offline against the manifest and the downloads tree — no session, no network:

| Class | Meaning |
| :-- | :-- |
| `ok` | final path present, size matches the manifest |
| `missing` | final path **and** blob are gone |
| `final-missing-blob-alive` | only the blob survives — repairable offline |
| `size-mismatch` | on-disk size drifted from the recorded size |
| `hash-mismatch` | (`--deep`) SHA-256 re-hash differs from the recorded digest |
| `format-error` | (`--deep`) native archive validation failed |
| `unchecked-format` | (`--deep`) extension has no native validator — a coverage note, never a failure |
| `orphan-blob` | blob-store bytes no manifest row references |
| `row-without-path` | queued/failed rows without a path — counted, skipped |

`--deep` validates archive formats **natively with the Go standard library —
no external ffmpeg/ffprobe/unrar**:

| Format | Check |
| :-- | :-- |
| `.zip` | `archive/zip` full walk — every member decompressed, per-member CRC32 verified |
| `.tar.gz` / `.tgz` / `.gz` | `compress/gzip` full decompress (CRC32 + ISIZE), tar headers walked when present |
| `.rar` | honest **lite** check: RAR4/RAR5 signature plus best-effort end-of-archive marker |
| other | size check only (`unchecked-format`) |

`--fix` repairs what it safely can: `final-missing-blob-alive` rows are
re-hardlinked from the live blob, `missing`/`size-mismatch`/`hash-mismatch`
rows are requeued for the next `dl`, orphan blobs go through the
`dedupe gc` path. `format-error` rows are never auto-repaired — corrupted
bytes need a human decision. Exit code is non-zero while problems remain.

---

## 🌐 Proxies & WEB-proxy v1

| Scheme | Transport |
| :-- | :-- |
| `socks5://user:pass@host:port` | SOCKS5 (auth supported) |
| `socks4://host:port` | SOCKS4 (in-house dialer) |
| `http://user:pass@host:port` | HTTP CONNECT (in-house dialer) |
| `mtproto://host:port/<hex-secret>` | MTProto proxy; `dd`-padded and `ee` fake-TLS secrets both supported |
| `webproxy://host:443/<secret>?carrier=websocket` | **WEB-proxy v1** (see below) |

Rotate by changing `--proxy`/config and reconnecting — the session survives;
**FloodWait does not** (it binds to the account, not the IP). `teleparse
proxy test` probes each scheme against the production DCs (connect time per
DC); `teleparse ping` adds RPC RTT using the logged-in session. Proxy
precedence: `--proxy` flag > `TELEPARSE_PROXY` > standard environment
(`HTTPS_PROXY`, `https_proxy`, `ALL_PROXY`, `all_proxy`) > config file;
`net.ignore_env` skips only the standard variables.

**WEB-proxy v1** is a first client implementation of Telegram's new
web-proxy protocol ([tproxy-server](https://github.com/telegramdesktop/tproxy-server),
Aug 2026): MTProto frames multiplexed (OPEN/DATA/WINDOW/CLOSE with
credit-based flow control) over an HTTPS or WebSocket carrier that looks like
ordinary browsing. Bootstrap: HMAC-SHA256 capability → bridge page → session
token. Carriers: `websocket` / `websocket-lanes` (primary), `https` /
`https-lanes` (fallback). Verified against the reference `tproxy-server` in
integration tests (`-tags webproxy_integration`).

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

---

## 🛡️ Anti-ban & Accounts

**Pacing.** Conservative defaults: 3 parallel downloads, 1–4 s jittered pause
between file starts, optional global token bucket
(`pacing.requests_per_minute`, 0 disables), per-file retries with exponential
backoff (`retry_max = 4`).

**FloodWait park/resume.** Waits ≤ `flood_sleep_threshold` (default 60 s)
auto-sleep and keep going; longer waits **park** the run with a `resume_at`
timestamp — `teleparse resume` (or `resume --all`) continues later exactly
where it stopped, thanks to `.part` offsets and the manifest.

**Auto-takeout.** Takeout mode is Telegram's sanctioned export path with
lower flood limits. With `takeout_auto = true` (the default), scopes
estimated at ≥ `takeout_auto_min_chats` (default 50) chats enter takeout
automatically; the export session finishes with the run. Export sessions
carry a per-account file cap — **2 GiB, or 4 GiB with Telegram Premium**
(picked up from the premium cache once detected): oversized files fail fast
with `TAKEOUT_FILE_TOO_BIG` in FAIL lines and the summary.

**Premium autodetect.** With `premium_boost = true` (the default), teleparse
detects Telegram Premium accounts on connect (cached 24 h per account) and
upgrades the default connection sizing to the premium preset — 8 pooled
connections per DC, matching TDLib's premium download envelope and lifting
the account-level media throttle. Explicit `connections` you set always wins;
`auth status` and `doctor` show the detected state. Failed detection falls
back to the cache (or non-premium) and never blocks a download.

**Per-chat account routing & premium fallback.** `[accounts]` (both features
**off by default**) splits one `dl`/`scan`/`sync` invocation across accounts —
same command, strictly sequential sessions (the per-account flock forbids
concurrency anyway):

- `routing = { "@chat" = "spare", "123456" = "spare" }` — a routed chat is
  excluded from the default account's pass and processed by a second client
  session of that account afterwards. Unknown routed accounts fail at start
  with the known-account list, never mid-run. `get` and `resume` ignore
  routing; `--account` pins one account and skips both; `--no-routing`
  bypasses routing for one run.
- `premium_preferred = true` — items larger than the current session's cap
  but within the premium 4 GiB cap defer to a final session of a local
  Telegram Premium account instead of failing oversized. Without a local
  premium session the failure (with its cap reason) stays.

**Freshness & history-clear detection.** `[scan] incremental = true` (the
default) caches per-chat watermarks — repeat runs walk only new messages.
`rewalk_min_age` (default `"10m"`; `"0"` disables) skips re-probing chats
walked more recently than the window, so a restarted run settles instantly.
If the dialogs page shows a chat's newest message id *below* its watermark,
the chat was mass-cleared: the watermark resets and the chat re-walks in
full. `--full` forces one complete re-walk of every chat.

---

## 🔌 Command Surface

| Command | Purpose |
| :-- | :-- |
| `auth login \| logout \| status \| list` | multi-account sessions (0600, flock, stable device identity); `--qr` QR login, `--import-telethon`, `--import-tdesktop` |
| `auth export` | export the raw session JSON (treat as a secret) |
| `chats list \| show` | dialogs with types, usernames, protected flags |
| `scan [CHATS] [FILTERS]` | `dl --dry-run`: writes manifest rows, downloads nothing |
| `dl [CHATS] [FILTERS]` | download; `--dry-run`, `--count-only`, `--takeout`/`--no-takeout`, `--full`, `--no-routing`, `--notify-webhook`, `--rewrite-ext` |
| `sync [CHATS]` | incremental via per-chat watermarks (cron-friendly) |
| `get [LINKS]... [FILTERS]` | one-shot fetch by t.me link, id list or range (`t.me/news/100-200`, `t.me/c/.../789`, `?thread=`, `tg://resolve?...`); album grouping `--group` (default); `--from-export` adopts a Telegram Desktop export — on-disk files are hardlinked in, only absent ones queue |
| `resume [RUN_ID]` | resume runs parked by FloodWait or interrupted; `--all` |
| `runs list \| show \| clean` | run history |
| `profile save \| list \| show \| rm` | named filter presets in config |
| `export jsonl \| csv` | manifest export |
| `stats` | download statistics per chat and totals |
| `verify [CHATS]` | offline integrity sweep; `--deep` native validation, `--fix` repairs |
| `dedupe stats \| gc` | inspect the hardlink blob store; `gc` frees unreferenced blobs |
| `proxy show \| test` | proxy config and DC probe (DCs 2–5) |
| `ping [--dc N\|all]` | connect time + RPC RTT to Telegram DCs via the session |
| `doctor` | config/credentials/session/disk/database/proxy diagnostics + premium status |
| `completion [shell]` | shell autocompletion scripts |

Chat specs: `all` · `@username` · `t.me/...` link · numeric id · `saved` ·
glob (`"News*"`); multiple specs per command, deduplicated by chat id.

Global flags:

| Flag | Purpose |
| :-- | :-- |
| `--account <name>` | account name (default from config) |
| `--config <path>` | config file path (default `~/.config/teleparse/config.toml`) |
| `--proxy <url>` | `socks5:// socks4:// http:// mtproto:// webproxy://` |
| `--root <dir>` | downloads root (overrides config) |
| `--format table\|json\|plain` | stable output format for `chats list`, `runs list\|show`, `stats`, `proxy show\|test`, `ping`, `auth list` |
| `--no-ascii` | plain ASCII output: line-per-item progress, ASCII tables/bars |
| `--no-color` | disable colors (also `NO_COLOR=1`, non-terminal stdout) |
| `-s`, `--silent` | suppress progress UI and per-item lines (on `dl`/`scan`/`sync` use `-s`; plain `--silent` there filters silently-sent messages) |
| `-v`, `--version` | version |

---

## ⚙️ Configuration

`~/.config/teleparse/config.toml` (auto-created as a fully commented
template on first run; unknown keys **rejected**). Precedence is strict:
`flags > env (TELEPARSE_*) > profile > file > defaults`.

Environment variable groups:

- **Credentials:** `TELEPARSE_API_ID`, `TELEPARSE_API_HASH` (else
  `credentials.toml`).
- **Run overrides:** `TELEPARSE_ACCOUNT`, `TELEPARSE_PROXY`,
  `TELEPARSE_TAKEOUT`, `TELEPARSE_CONCURRENCY`, `TELEPARSE_ROOT`,
  `TELEPARSE_WEBHOOK`, `TELEPARSE_PROFILE`.
- **Standard proxy env** (when no explicit override):
  `HTTPS_PROXY`, `https_proxy`, `ALL_PROXY`, `all_proxy` — skip with
  `net.ignore_env`.

Full annotated `config.toml` (defaults shown):

```toml
[auth]
account = "default"              # default account name

[net]
proxy = ""                       # socks5:// socks4:// http:// mtproto:// webproxy://
ignore_env = false               # skip standard proxy env vars (TELEPARSE_PROXY still wins)
takeout = false                  # always wrap session in takeout (export) mode
takeout_auto = true              # engage takeout when a scan looks large
takeout_auto_min_chats = 50      # estimated scope size that triggers auto takeout

[pacing]
concurrency = 3                  # parallel downloads
delay_min = 1.0                  # jittered pause between file starts, seconds
delay_max = 4.0
flood_sleep_threshold = 60       # <=: auto-sleep; longer FloodWait parks the run
requests_per_minute = 0          # global token bucket; 0 disables
retry_max = 4                    # attempts per file (exponential backoff)

[download]
threads = 4                      # compatibility knob; no engine effect (transfer is sequential ranged)
connections = 3                  # pooled MTProto connections per DC (1-8)
premium_boost = true             # premium preset (connections 8) on auto-detected premium accounts

[scan]
incremental = true               # watermark-cached walks; --full overrides per run
rewalk_min_age = "10m"           # per-chat walk freshness window; "0" = walk every run

[accounts]                       # both features off by default
routing = { "@chat" = "spare" }  # per-chat account routing (spec -> account)
premium_preferred = false        # oversized (2-4 GiB) items defer to a local premium session

[output]
root = ""                        # "" = ~/.local/share/teleparse/downloads
template = "{chat}/{date:%Y-%m}/{filename}"
collision = "index"              # index | overwrite | skip
naming = "original"              # original | msgid (<msgID>_<index><ext>)
metadata = "chat"                # chat | file | off (sidecar bool maps here)
sidecar = true                   # deprecated: maps to metadata = "file"/"off"
part_suffix = ".part"            # in-progress suffix
sha256 = false                   # hash after download
rewrite_ext = false              # canonical extension for recorded mime type

[run]
notify_webhook = ""              # POST JSON run summary at run end / flood park

[hooks]
post_download = []               # run after each successful download; {path} templated

[filters]                        # defaults; profiles overlay these
dedupe = "hardlink"              # hardlink | unique-id | hash | off

[filters.recursion]
topics = true                    # recurse into forum topics
albums = "expand"                # expand | first | skip

[profiles.videos]                # named overlay: teleparse dl --profile videos
media = ["video"]
min_size = "5MB"
```

---

## 🔒 Security

- **Session hygiene** — per-account sessions live at
  `~/.config/teleparse/accounts/<name>/session.json` with `0600`
  permissions inside a `0700` directory, guarded by a single-process
  `flock` so two teleparse runs never share one session.
- **Credentials** — API id/hash resolve from environment or
  `~/.config/teleparse/credentials.toml` (`0600`, dir `0700`); `auth login`
  writes the file only on explicit consent. `doctor` reports the resolved
  source and never prompts.
- **Stable device fingerprint** — each account derives a realistic client
  fingerprint once, persists it and reuses it verbatim, so logins never
  drift across machines or proxies.
- **No secrets in the repository** — `.env`, `*.session`, `accounts/` and
  `state.db*` are gitignored; `auth export` prints the raw session JSON and
  labels it a secret.
- **Proxy secrets** — proxy URLs may embed credentials; they are supplied
  via flag, `TELEPARSE_PROXY` or config and are not written to the manifest
  or downloaded metadata.
- **Anti-lockdown defaults** — conservative pacing, takeout for large
  scopes, and account-bound FloodWait handling (proxy rotation never
  launders a throttle) keep archival runs inside Telegram's tolerance.

---

## 🚀 Quick Start

### 1. Install

Release binary (linux/amd64 one-liner; other platforms on the
[Releases](https://github.com/4q4r/teleparse/releases) page with SHA-256
`checksums.txt`):

```bash
curl -fsSL https://github.com/4q4r/teleparse/releases/latest/download/teleparse_Linux_x86_64 \
  -o teleparse && chmod +x teleparse && ./teleparse -v
```

Via `go install` (private repo — GOPRIVATE + SSH auth, no global git config
changes):

```bash
GOPRIVATE=github.com/4q4r/* \
GIT_CONFIG_COUNT=1 \
GIT_CONFIG_KEY_0='url.git@github.com:.insteadOf' \
GIT_CONFIG_VALUE_0='https://github.com/' \
go install github.com/4q4r/teleparse/cmd/teleparse@latest
```

From source:

```bash
git clone git@github.com:4q4r/teleparse.git && cd teleparse
make build          # CGO_ENABLED=0 static binary → ./teleparse
```

### 2. First run

```bash
# 1. credentials from https://my.telegram.org (export, or let login prompt and save them)
export TELEPARSE_API_ID=123456
export TELEPARSE_API_HASH=abcdef1234567890abcdef1234567890

teleparse auth login                # 2. phone → code → 2FA (or: --qr, --import-telethon, --import-tdesktop)
teleparse chats list --type private # 3. see what's accessible (warms the dialog cache)
teleparse scan all --media photo --last 7d --explain   # 4. dry-run: plan + pushdown report, no downloads
teleparse dl @durov --media video --min-size 5MB       # 5. real download
teleparse verify                    # 6. offline integrity sweep of what landed
```

Add a second account with `teleparse auth login --account spare`, then use
`--account spare|all` anywhere — or map specific chats to it via
`[accounts] routing`.

---

## 💻 Local Development

`Makefile` targets:

| Target | What it runs |
| :-- | :-- |
| `make build` | `CGO_ENABLED=0` static binary with version ldflags |
| `make fmt` | `golangci-lint fmt` (gofumpt-class formatting) |
| `make lint` | `golangci-lint run` — strict ~70-linter config, 0 findings required |
| `make vet` | `go vet ./...` |
| `make test` | `go test -race -count=1 ./...` |
| `make cover` | coverage profile + per-function totals (`coverage.out`) |
| `make vuln` | `govulncheck ./...` |
| `make integration` | `go test -race -tags webproxy_integration ./internal/webproxy/...` (needs Docker) |
| `make check` | fmt + lint + vet + test + vuln in one shot — the gate |
| `make release-snapshot` | GoReleaser dry run |

Requires Go 1.27+ (see `go.mod`), golangci-lint v2. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the full guide: dev setup, the gate,
conventional commits and the `webproxy_integration` tag.

---

## ✅ Testing

The gate (mirrors CI):

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -count=1 ./...          # 1357 tests across 14 packages
PATH=$PATH:~/.local/bin golangci-lint run ./...   # strict config, 0 findings
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
go test -race -count=1 -tags webproxy_integration ./internal/webproxy/...  # Docker
```

Numbers as of this release: **1357 tests, 14 packages, all passing with
`-race`**; golangci-lint strict configuration with **0 findings**. `.github/
workflows/ci.yml` runs lint, vet, race tests, govulncheck, the WEB-proxy
integration suite (against the reference `tproxy-server` via Docker) and a
static build on every push and PR; releases are cut by GoReleaser on `v*`
tags.

---

## 📁 Filesystem Layout

| Path | Description |
| :-- | :-- |
| `~/.config/teleparse/config.toml` | main config (auto-created, annotated) |
| `~/.config/teleparse/credentials.toml` | API id/hash (`0600`) |
| `~/.config/teleparse/accounts/<name>/session.json` | per-account session (`0600`, flock-guarded) |
| `~/.local/share/teleparse/state.db` | SQLite WAL: manifest, watermarks, runs |
| `~/.local/share/teleparse/downloads/` | default downloads root (`output.root`) |
| `<root>/.teleparse/blobs/<class>/<id><ext>` | hardlink blob store |
| `<root>/<chat>/manifest.json` | per-chat download metadata (`metadata = "chat"`) |
