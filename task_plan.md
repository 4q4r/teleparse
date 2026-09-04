# teleparse — Telegram userbot parser CLI

Production-grade CLI: downloads any media (photos, videos, voice, video notes, files) from any chats
accessible by personal Telegram accounts, with a ~150-dimension filter engine, multi-account support,
anti-ban pacing, and full proxy support (SOCKS4/5, HTTP CONNECT, MTProto dd+ee, and the new WEB-proxy v1).

## Stack (researched & pinned, 2026-09-04)

| Component | Choice | Version |
|---|---|---|
| Language | Go | 1.25 |
| MTProto | gotd/td | v0.161.0 (Layer 228) |
| Middlewares/sessions | gotd/contrib | v0.25.0 |
| CLI | spf13/cobra | v1.10.2 |
| TUI progress | charmbracelet/bubbletea/bubbles | v2 |
| SQLite | modernc.org/sqlite | v1.58.0 (CGo-free) |
| WebSocket | coder/websocket | v1.8.15 |
| SOCKS5 | golang.org/x/net | v0.58.0 |
| TOML | pelletier/go-toml/v2 | v2.4.3 |
| Regex (lookarounds) | dlclark/regexp2 | v1.12.0 |

## Phases

- [x] 0. Foundation: git init, go.mod, layout, spec, lint tooling, README skeleton
- [x] 1. Config + CLI skeleton (TOML, profiles, precedence, cobra tree, doctor)
- [x] 2. Auth + multi-account (sessions 0600+flock, login flow, Telethon import, chats list)
- [x] 3. Scan + filter engine (~150 predicates, pushdown, walker, --explain/--dry-run/--count-only)
- [x] 4. Download + state (SQLite WAL, .part resume, dedup, path templates, sidecars, hooks)
- [x] 5. Sync/resume/runs/export/stats
- [x] 6. Proxies (socks5/4, http CONNECT, mtproto dd/ee, rotation, proxy test)
- [x] 7. WEB-proxy v1 (frame codec, WINDOW credits, carriers ws+https, bootstrap, dcs.Resolver)
- [x] 8. Anti-ban polish + full quality gates + README

## Decisions

- 2026-09-04: Go+gotd over Rust+grammers (WEB-proxy needs pluggable transport; gotd has dcs.Resolver+transport.Conn verified; grammers hardcodes transport::Full)
- 2026-09-04: WEB-proxy in v1 (user hard requirement), carriers: websocket primary, https fallback
- 2026-09-04: Multi-account sessions in ~/.config/teleparse/accounts/<name>/ (user requirement)
- 2026-09-04: State SQLite at ~/.local/share/teleparse/state.db, overridable
- 2026-09-04: modernc.org/sqlite (CGO_ENABLED=0 static binary)
- 2026-09-04: gotd pinned v0.161.0 exactly (pre-1.0 churn), isolated behind internal/tg gateway

## Errors Encountered

| Error | Attempt | Resolution |
|-------|---------|------------|

## DONE 2026-09-04
Все 9 фаз (0-8) реализованы. 567 тестов, lint 0, vuln 0 (код), статик-бинарь 21MB.
Коммиты: 9ca7eaf, e2bc1ad, 4731636 (tg), 5ed2499 (webproxy+интеграция), d8f4400+4859123 (filters), 467085c (store/pace/export), 7b73c4e (scan), b063e45 (download+CLI), финал (profile+README).
