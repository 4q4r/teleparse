# findings.md — verified facts (Exa/primary-source, 2026-09-04)

## gotd/td v0.161.0 (source-verified)
- Layer 228 (`tg/tl_registry_gen.go:35 const Layer = 228`); 800 generated tg.Client methods.
- Transport seams: `telegram/dcs.Resolver` interface `Primary/MediaOnly/CDN(ctx, dc int, list List) (transport.Conn, error)`; `DialFunc`; `transport.Conn` = `Send(ctx,*bin.Buffer)/Recv(ctx,*bin.Buffer)/Close()` (NOT net.Conn).
- Built-in resolvers: `dcs.Plain(PlainOptions{Protocol: Intermediate|Abridged|Padded})`, `dcs.Websocket(WebsocketOptions)`, `dcs.MTProxy(addr, secret, MTProxyOptions)`, `dcs.DNS`, `dcs.List`, `dcs.DefaultResolver()`.
- `mtproxy` package: `Secret{Simple, Secured=dd, TLS=ee}`, `obfuscator.Obfuscated2/FakeTLS` exported → reusable for WEB-proxy.
- Downloader: `downloader.NewDownloader().WithPartSize/WithAllowCDN/WithRetryHandler.Download(...)` → Builder `.WithThreads(n)/WithVerify/.Stream(ctx, io.Writer)/.Parallel(ctx, io.WriterAt)/.ToPath`; CDN handled (cdn.go).
- FloodWait: `tgerr.AsFloodWait`, `tgerr.FloodWait(ctx, err)`; client hook `telegram/flood_wait.go`; contrib `middleware/floodwait` (Waiter/SimpleWaiter/WithMaxRetries/MaxWait), `middleware/ratelimit`.
- Sessions: `session.Storage`; `session.FileStorage`; tdesktop + telethon importers exist; multi-account = N clients.
- Device spoof: `telegram.Device DeviceConfig{DeviceModel, SystemVersion, AppVersion, SystemLangCode, LangPack, LangCode}`; `examples/tdesktop-mimic`.
- Iterators: `telegram/query/{messages,dialogs,channels,contacts,photos}`; `query/messages/media_group.go` (albums). 27 `inputMessagesFilter*` in schema.
- Middleware: `telegram.Options.Middlewares []Middleware`; `type Middleware interface { Handle(next tg.Invoker) InvokeFunc }`.
- go.mod: Go 1.25; pure-Go deps (uTLS, coder/websocket) → CGO_ENABLED=0 OK.

## WEB-proxy v1 protocol (tproxy-server PROTOCOL.md + server.go, source-verified)
- capability = base64url(HMAC-SHA256(secret, "tdesktop-web-proxy-bridge-v1\n"+host)); GET https://H/?bridge=<cap>; relay mints 2-min bootstrap token embedded in bridge page (server.go:170 IssueBootstrap → bridge.Render).
- POST /api/v1/session (Bearer bootstrap, HELLO frame) → session token + X-Carrier-Mode.
- Carriers: ws GET /api/v1/ws subprotocol `tproxy-v1.<token>`; lanes `tproxy-lane-v1.<token>.<stream>`; https POST /api/v1/up (X-Up-Seq) + long-poll POST /api/v1/down (X-Down-Cursor).
- Frames: u8 type | u24 stream | u32 len | payload. OPEN/DATA/WINDOW/CLOSE/PING/PONG/HELLO/WELCOME/BYE. 4 MiB initial window, 64 KiB DATA chunks, 1 MiB max payload, 4096 tombstones. Client keeps normal MTProxy transform.
- Origin NOT authenticated; spec anticipates non-browser clients. Session loss = recreate (no resume). PoC stage.

## Versions (proxy.golang.org / crates.io / PyPI JSON, 2026-09-04)
- gotd/td v0.161.0 (2026-07-14) · gotd/contrib v0.25.0 (2026-07-15) · x/net v0.58.0 · coder/websocket v1.8.15 · cobra v1.10.2 · bubbletea v2.0.9 / bubbles v2.2.1 / lipgloss v2.0.6 · modernc.org/sqlite v1.58.0 · pelletier/go-toml/v2 v2.4.3 · dlclark/regexp2 v1.12.0 · BurntSushi/toml v1.6.0 · dustin/go-humanize v1.0.1 · gorilla/websocket STALLED (avoid) · 33TU/socks v0.4.0 (license TBD at impl) · bdandy/go-socks4 v1.2.3 MIT.
- FloodWait = account-bound, NOT IP-bound; takeout = lower flood limits (core.telegram.org/api/takeout).
- Telethon sessions don't store messages; entity cache is the only deleted-channel recovery.

## Filter mapping sources
- Telethon objects-ref, core.telegram.org schema @ Layer 223+ (message#3ae56482, messageMediaDocument video/round/voice flags, document#8fd4c4d8, user#31774388, dialog, dialogFilter, MessageReactions, forumTopic), 27 InputMessagesFilter*, getSearchCounters.
- gotd equivalents: tg.Message.GroupedID/Reactions, tg.DocumentAttribute*, tg.MessageReplyHeader (ForumTopic flag, ReplyToTopID), tg.MessageFwdHeader, peers package, fileid package.
