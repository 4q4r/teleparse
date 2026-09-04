# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-09-04

First public release.

### Added

- **Multi-account userbot parser**: log in with several personal Telegram
  accounts (`auth login --account …`), switch or fan out across them
  (`--account a,b|all`); sessions stored `0600` with per-account stable device
  identity and single-process locking.
- **Filter engine, ~150 combinable dimensions**: media type, file metadata
  (mime/ext/name/size/duration/resolution), text & entities, dates, forwards,
  engagement, chat and sender predicates, id ranges and execution options.
  Supported filters are pushed down to Telegram's servers
  (`InputMessagesFilter*`, date bounds) where possible; `--explain` shows the
  exact pushdown/client-side split plus known server quirks.
- **Proxies**: SOCKS4/5 (auth), HTTP CONNECT, MTProto (`dd`-padded and `ee`
  fake-TLS secrets), and **WEB-proxy v1** (`tproxy` client) — MTProto frames
  multiplexed over HTTPS/WebSocket carriers with credit-based flow control.
  First third-party client implementation of the protocol, integration-tested
  against the reference `tproxy-server`.
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
- **Doctor**: config/credentials/disk/DB/proxy diagnostics.

<!-- TODO: replace OWNER with the real GitHub owner once the repo URL is public.
     From the next release on, link compare URLs, e.g.
     https://github.com/OWNER/teleparse/compare/v0.1.0...v0.2.0 -->

[0.1.0]: https://github.com/OWNER/teleparse/compare/v0.1.0
