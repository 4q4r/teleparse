# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Hardlink dedupe (new default)**: `dedupe = "hardlink"` downloads each unique
  Telegram file once into the blob store under `<root>/.teleparse/blobs` and
  hardlinks it into every chat that sighted it — zero extra network/disk for
  repeats, and deleting any subset of the copies never breaks the survivors.
  `teleparse dedupe stats | gc` inspects and cleans the blob store. On
  filesystems without hardlink support links degrade to byte copies.
  The `unique-id`, `hash` and `off` modes behave as before.

### Changed

- Default `filters.dedupe` is now `hardlink` (was `unique-id`); duplicate
  sightings are linked into their chats instead of skipped.
- The global `--root` flag now also updates the resolved downloads root;
  previously only `output.root` from the config file took effect.

## [0.2.0] - 2026-09-20

Feature release: 36 commits since 0.1.1 (PRs #26–#45).

### Added

- **`teleparse get`** — one-shot fetch by t.me links: public/private channels,
  forum topics, `?thread=`/`?comment=`, id lists and ranges, `tg://resolve`;
  albums auto-expand (`--group`); `--from-export` ingests a Telegram Desktop
  `result.json` with on-disk **adoption** (existing files hardlink into the
  blob store, only missing ones download).
- **`teleparse verify`** — offline integrity sweep (manifest vs disk):
  ok / missing / final-missing-blob-alive / size- / hash-mismatch /
  orphan-blob classes; `--deep` adds **native** archive validation
  (zip full-walk CRC32, gzip/tar.gz full decompression, RAR signature-lite —
  no external tools); `--fix` relinks from live blobs, requeues the rest,
  GCs orphaned blobs.
- **Hardlink deduplicator** (`dedupe = "hardlink"`, new default): one download
  into a canonical blob store, per-chat hardlinks — deleting any subset of
  copies never breaks survivors; `teleparse dedupe stats | gc`.
- **Incremental walks** (`[scan] incremental = true`): per-chat watermarks,
  cached-count surfacing, history-clear detection (newest-id < watermark →
  full re-walk), `--full` opt-out; **walk freshness** (`rewind` →
  `rewalk_min_age = "10m"`): restarts skip recently-walked chats entirely.
- **Ranged sequential download engine** (universal): 512 KiB ranged requests
  resuming at the exact on-disk offset; connection drops, EPIPE from local
  proxies and engine cancellations retry per-chunk and stay resumable —
  resumed files never re-download from zero.
- **Naming & metadata modes**: `naming = "original" | "msgid"` (cached items
  resolve real `{chat}/{date}/{filename}` paths from manifest data — the
  `<chatID>/<msgID>` fallback effectively never fires); `metadata = "chat"`
  (default) writes one numbered `manifest.json` per chat instead of per-file
  sidecars (`file` legacy, `off`).
- **Auto-takeout hardening**: `file_max_size` caps (2/4 GiB premium-aware) —
  fixes instant `TAKEOUT_FILE_TOO_BIG` on every file; TAKEOUT_INIT_DELAY
  falls back to plain mode; finish-phase failures never fail a successful
  run.
- **Per-chat account routing + premium-preferred** (`[accounts]`, off by
  default); **`--notify-webhook`** (done/parked events with top failure
  reasons); **`--rewrite-ext`** (canonical extension by MIME).
- uv-style live progress for walk AND download phases, stalled markers,
  FAIL lines keep the error tail, `--format table|json|plain`, `--no-color`,
  QR/2FA login completion, per-chat JSON sidecar data in manifests.

### Fixed

- Unique-file conflicts across chats never abort runs (idempotent upsert);
  blank chat titles render `chat_<id>`; progress never exceeds 100%
  (high-water-mark accounting); `get`-style cached-item hydration via message
  refetch (`no file location` bug); downloads ride the takeout invoker
  (raw-pool 403s); doubled `.exe` suffix on Windows release binaries.

[0.2.0]: https://github.com/4q4r/teleparse/releases/tag/v0.2.0

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
