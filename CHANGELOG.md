# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-09-05

### Changed

- Module path renamed `teleparse` → `github.com/4q4r/teleparse`: the tool is
  now installable via
  `GOPRIVATE=github.com/4q4r/* go install github.com/4q4r/teleparse/cmd/teleparse@latest`
  (binary lands in `$(go env GOPATH)/bin`).

### Fixed

- Windows release binaries carried a doubled `.exe` suffix.

## [0.1.0] - 2026-09-05

First public release.

### Added

- **Multi-account userbot parser**: log in with several personal Telegram
  accounts (`auth login --account …`), switch or fan out across them
  (`--account a,b|all`); sessions stored `0600` with per-account stable device
  identity and single-process locking.
- **Filter engine, ~150 combinable dimensions**: media type, file metadata
  (mime/ext/name/size/duration/resolution), text & entities, dates, forwards,
  engagement, chat and sender predicates, id ranges and execution options.
  Include AND exclude families (`--exclude-media/-mime/-ext/-chat-type`),
  inverse sender selection (`--sender-non-contacts/-non-mutual`), deleted-account
  scoping (`--chat-deleted`, `--sender-deleted`), sender matching by id,
  @username or phone number. Supported filters are pushed down to Telegram's
  servers (`InputMessagesFilter*`, date bounds) where possible; `--explain`
  shows the exact pushdown/client-side split plus known server quirks.
  Scope multiple specific chats at once: `dl @a @b 12345 'News*' saved`.
- **Proxies**: SOCKS4/5 (auth), HTTP CONNECT, MTProto (`dd`-padded and `ee`
  fake-TLS secrets), and **WEB-proxy v1** (`tproxy` client) — MTProto frames
  multiplexed over HTTPS/WebSocket carriers with credit-based flow control.
  First third-party client implementation of the protocol, integration-tested
  against the reference `tproxy-server`. Automatic pickup of standard proxy
  environment variables (`TELEPARSE_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, …)
  with `net.ignore_env` escape hatch and `proxy show` source reporting.
- **Download engine**: parallel per-DC connection pools (`client.MediaOnly`,
  FILE_MIGRATE-aware) + per-file threads — fixes gotd's single-connection
  multiplexing cliff; `[download] threads/connections` with official-client
  defaults (4/3) and a turbo envelope (8/6–8). Server throttling
  (`FLOOD_WAIT`/`FLOOD_PREMIUM_WAIT`) surfaced live and auto-retried.
- **Premium autodetect**: premium accounts (Self query, 24h cache) are boosted
  to the premium envelope (8 connections / 8 threads) automatically unless
  custom values are set; shown in `auth status` and `doctor`.
- **CLI UX**: live progress UI (uv-style per-file bars, speed, ETA, throttle
  notices; TTY-aware), `-v`/`-h`/`-s` silent mode, `--format table|json|plain`
  across list commands, `--no-ascii` plain output, `teleparse ping` with
  per-DC connect time and RPC RTT.
- **Config**: auto-generated, fully commented `~/.config/teleparse/config.toml`
  template (16+ documented options); strict `flags > env > profile > file`
  precedence; unknown keys rejected.
- **Anti-ban pacing**: conservative defaults (3 parallel, jittered delays),
  optional global token bucket; `FloodWait` auto-sleep below threshold, park +
  `resume` above it; `--takeout` wraps the session in Telegram's sanctioned
  export mode.
- **SQLite state store** (WAL): per-chat watermarks for incremental `sync`,
  idempotent run resume, per-media progress (`bytes_done`, `.part` files,
  atomic rename), and global dedup by stable Telegram file identity with
  optional SHA-256.
- **Telethon session import**: `auth login --import-telethon session.sqlite`
  migrates an existing auth key.
- **Exports & stats**: manifest export to JSONL/CSV; per-chat totals.
- **Doctor**: config/credentials/disk/DB/proxy/premium/RTT diagnostics.

### Fixed

- `openPartFile` did not create parent directories — templated downloads
  failed with ENOENT.
- Retry ladder could hand one media item to two workers (double-claim race);
  claim ownership is now held until exhaustion.
- `floodwait` middleware retried `FLOOD_WAIT` indefinitely, making the
  documented park/resume path unreachable; now bounded by
  `flood_sleep_threshold`.
- WEB-proxy stream close race: post-EOF writes could land before the window
  abort published.

[0.1.0]: https://github.com/4q4r/teleparse/releases/tag/v0.1.0
