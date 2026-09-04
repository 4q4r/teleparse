# teleparse — Design Spec

Date: 2026-09-04 · Status: APPROVED · Implementation: phases 0–8 (see task_plan.md)

## 1. Product

Telegram **userbot** parser CLI: downloads any media (photos, videos, voice, video notes/кружки,
documents, stickers, GIFs, audio) from any chats accessible by **personal accounts** (MTProto, no Bot API),
with a ~150-dimension filter engine, simultaneous **multi-account** sessions, anti-ban pacing, and full
proxy support: SOCKS4/5, HTTP CONNECT, MTProto-proxy (`dd` + `ee` fake-TLS), and the new **WEB-proxy v1**
(tproxy-server protocol, Aug 2026).

## 2. Stack (verified via primary sources 2026-09-04)

| Component | Choice | Version | Rationale |
|---|---|---|---|
| Language | Go | 1.25+ (toolchain 1.27 present) | static binary, goroutines, only lib with pluggable transport |
| MTProto | gotd/td | v0.161.0 | Layer 228; `dcs.Resolver`+`transport.Conn` verified pluggable; downloader.Parallel; mtproxy dd/ee |
| Middlewares | gotd/contrib | v0.25.0 | floodwait + ratelimit |
| CLI | spf13/cobra | v1.10.2 | mature command tree |
| Progress | charmbracelet/bubbletea | v2.x | multi-bar progress |
| SQLite | modernc.org/sqlite | v1.58.0 | pure Go → CGO_ENABLED=0 |
| WebSocket | coder/websocket | v1.8.15 | WS carrier for WEB-proxy (already a gotd dep) |
| SOCKS | golang.org/x/net (v5) + socks4 lib | v0.58.0 | SOCKS5 built-in; SOCKS4 via small lib (license-verified at impl) |
| TOML | pelletier/go-toml/v2 | v2.4.3 | config |
| Regex | dlclark/regexp2 | v1.12.0 | lookarounds/backrefs (gotd dep already) |

Rejected: Python (user), Rust+grammers (hardcoded `transport::Full` → fork tax; no MTProto-proxy;
SOCKS5-only), TDLib (no WEB-proxy; dead/stale bindings; C++ .so).

## 3. Layout

```
cmd/teleparse/          # main
internal/
  cli/                  # cobra commands, rich output
  config/               # TOML + profiles; precedence: flags > env(TELEPARSE_*) > profile > file > defaults
  tg/                   # gateway over gotd: account manager, sessions, device spoof, iterators
  webproxy/             # WEB-proxy v1 client: frame codec, WINDOW credits, carriers, bootstrap, resolver
  filters/              # predicate registry (~150 dims): server pushdown map + client predicates
  scan/                 # scope resolver (dialogs/contacts/topics/albums/replies), history walker
  download/             # worker pool, .part resume, path templater, hooks, sidecars
  store/                # SQLite WAL: chats/runs/media, watermarks, dedup
  pace/                 # FloodWait coordinator, token bucket, jitter, takeout
  export/               # JSONL/CSV
```

## 4. CLI surface

```
teleparse auth login|logout|status|list|export [--account]
teleparse chats list|show [--type ...] [--contacts-only] [--json]
teleparse scan [CHATS] [FILTERS]            # = dl --dry-run; writes plan + manifest rows
teleparse dl [CHATS] [FILTERS] [--account a,b|all] [--takeout] [--dry-run] [--explain]
teleparse sync [CHATS] [--profile P]        # incremental watermark mode (cron-friendly)
teleparse resume [RUN_ID] [--all]
teleparse runs list|show|clean
teleparse profile save|list|show|rm
teleparse export jsonl|csv [--run R] [--chat C]
teleparse stats [--chat C]
teleparse proxy test|show
teleparse doctor
```

Chat spec: `all` | `@username` | t.me link | numeric id | `saved` | glob over titles (`"News*"`).

Example (mission query):
`teleparse dl --chat-type private --sender-contacts --media document --mime application/zip --max-size 20MB`

## 5. Filter engine

- Each filter: `name + CLI flag + config key + predicate(MediaContext) -> bool`; AND composition, `--or` groups.
- **Server pushdown first**: 27 `tg.InputMessagesFilter*`, `search(q, min/max_date, from_id)`,
  `getSearchCounters` → minimal request volume. Everything else: client-side predicates.
- Dimensions (~150): media types (photo/video/voice/video_note/document/sticker×3/gif/audio/poll/geo/
  contact/dice/webpage/paid/...), file metadata (mime glob, ext, name glob/regex, size min/max **before**
  download, duration, WxH, codec, nosound, TTL, spoiler, waveform), message attrs (dates ISO+relative,
  edit date, text regex, entity-based hashtag/mention/URL/email/phone/bot-cmd, emoji-only, forwards
  origin/hidden/date, replies incl. depth & cross-chat & quotes, pinned, views/forwards/comments/reactions,
  service msgs, grouped_id albums, id ranges, forum topics, scheduled, noforwards, factcheck, paid stars),
  chat dims (private/group/supergroup/channel/forum/gigagroup, contacts-only, archived, folders, title
  regex, username, mute, membership, unread, member count, verified/scam/fake), sender dims (contact,
  mutual, close-friend, bot, premium, verified, scam, deleted, support, restricted, phone, name/username
  regex, online status, via_bot, anon admin), meta (limit=scan budget, recursion: topics/replies/albums/
  forward unpack, dedup unique-id|hash, skip-existing, offset/min-max-id, incremental since-state).
- Trap disambiguation: `--was-mentioned` ≠ `--text-mention`; `--media contact` (contact cards) ≠
  `--sender-contacts`; `--folder contacts` (user folder).
- Transparency: `--explain` (server vs client + est. requests), `--dry-run`, `--count-only`.
- Known server quirks (encode in pushdown planner + explain warnings): `from_id` ignored in private chats;
  `reply_to` iteration disables filter/search; `max_date` ignored with bare filter; low `min_id` emulated
  client-side; file_reference expiry → refetch.

## 6. WEB-proxy v1 (internal/webproxy)

Protocol source: `telegramdesktop/tproxy-server` PROTOCOL.md (fetched fresh at impl time; frozen-v1).

- Bootstrap: capability = base64url(HMAC-SHA256(secret, "tdesktop-web-proxy-bridge-v1\n"+host));
  GET `https://H/?bridge=<cap>` → parse bootstrap token from bridge page → POST `/api/v1/session`
  (Bearer, HELLO frame) → session token + X-Carrier-Mode.
- Carriers: `websocket` / `websocket-lanes` (GET `/api/v1/ws`, subprotocol `tproxy-v1.<token>`; lanes:
  `tproxy-lane-v1.<token>.<stream>`) primary; `https` / `https-lanes` (POST `/api/v1/up` with X-Up-Seq;
  long-poll POST `/api/v1/down` with X-Down-Cursor) fallback.
- Frames: `u8 type | u24 stream_id | u32 len | payload`; OPEN/DATA/WINDOW/CLOSE/PING/PONG/HELLO/WELCOME/BYE.
  Flow control: WINDOW credits (initial 4 MiB, 64 KiB DATA chunks, max payload 1 MiB, coalescing,
  4096 tombstones). Session loss = full recreate (no resume).
- Integration: custom `dcs.Resolver` returning `transport.Conn` (Send/Recv/Close over bin.Buffer) that
  multiplexes streams over the carrier session; MTProto transform **stays** the standard MTProxy
  obfuscation (reuse gotd `mtproxy` package obfuscator where applicable).
- Config: `webproxy://host:secret?carrier=websocket|https|auto`.
- Tests: golden frame codec tests (test vectors from PROTOCOL.md); integration vs local `tproxy-server`.

## 7. Proxies (internal/tg + config)

`socks5://user:pass@h:p` (x/net/proxy), `socks4://` (verified lib or in-house ~90 LOC),
`http://` (in-house CONNECT dialer ~60 LOC), `mtproto://h:p/secret` (dd+ee via `dcs.MTProxy`),
`webproxy://` (§6). Rotation: swap resolver + reconnect (auth key survives; no re-login). Truth documented:
FloodWait is account-bound, not IP-bound.

## 8. State & resume

SQLite (WAL) at `~/.local/share/teleparse/state.db` (overridable): separate from gotd sessions
(`~/.config/teleparse/accounts/<name>/`, 0600, flock).

Schema: `chats(chat_id PK, type, title, username, last_seen_message_id, last_synced_at)` —
watermark = max **contiguous** processed id; `media(chat_id, message_id, media_index, media_class,
media_id UNIQUE(media_class, media_id), mime, size, date, sender_id, filename, grouped_id, status,
attempts, last_error, path, bytes_done, sha256, updated_at)`; `runs(run_id PK, account, profile,
filter_json, status, resume_at, started_at, finished_at)`.

`.part` resume: `downloader.Parallel(ctx, io.WriterAt)` at offset min(bytes_done, part_size) truncated
to chunk multiple; atomic rename on completion; optional sha256; sidecar JSON per message;
file_reference refetch on expiry. Idempotent re-runs via PK + UNIQUE.

## 9. Anti-ban

Contrib floodwait middleware + ratelimit token bucket + own jitter scheduler (pause-broadcast to
workers on FloodWait; park run with resume_at if wait > threshold). Defaults: 3 concurrent downloads,
1–4s jittered delays. `--takeout` wraps client in takeout session (lower limits, legit export path).
Stable DeviceConfig per account. Entity-cache persistence (deleted channels: server won't return history —
our manifest is the only recovery; documented limitation).

## 10. Non-goals

Sending messages, stories posting, media re-upload, Bot API, GUI (TUI progress only), auto-updating.

## 11. Quality gates (every phase)

gofumpt formatting (via golangci-lint) = clean; `go vet`; staticcheck (via golangci-lint strict);
`golangci-lint` 0 findings; `go test -race -count=1 ./...` green; `govulncheck ./...` clean;
`go mod tidy` deterministic; new deps only after Exa license/maintenance verification; secrets never
committed (env/config gitignored).
