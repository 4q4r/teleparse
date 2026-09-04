# progress.md — session log

## 2026-09-04

- 4 research agents completed (libs/filters/proxy+antiban/architecture) — Python+Telethon recommended initially.
- USER PIVOT: no Python → Go/Rust. 2 research agents: Go+gotd WINS (pluggable transport, no fork, dd/ee built-in).
- USER APPROVED: Go 1.25 + gotd/td; WEB-proxy in v1; multi-account ~/.config/teleparse; production quality, continuous execution.
- Plan mode exited. Phase 0 started: git init (main), dirs, .gitignore, spec, findings, task_plan.
- Env: go1.27.0-X toolchain, golangci-lint ~/.local/bin, govulncheck ~/go/bin, docker OK. gofumpt/staticcheck standalone missing → via golangci-lint.

## Session 2 (продолжение)
- Фаза 0+1: scaffold готов — config (TOML/env/profiles/XDG/strict decode), filters Options (~150 полей, reflection-flags), cobra-дерево. 
- Ворота: golangci-lint strict (70+ линтеров) = 0, vet 0, tests 43 passed -race, govulncheck clean. Коммит 9ca7eaf.
- Уроки стиля: wsl (пустые строки перед блоками/после групп), varnamelen (имена >=3 симв), err113 (sentinel+wrap), gofumpt через golangci fmt, testifylint, t.Setenv без t.Parallel.
- bdandy/go-socks4 тянет древний x/net → выкинут, SOCKS4 будет in-house.
- x/net пиннут v0.58.0, testify v1.12.1.
- Диспатч волны 1: 3 субагента параллельно (tg-gateway / filters-engine / webproxy).

## Волна 1 завершена
- tg (4731636): аккаунты+flock+device-id, логин/2FA, SOCKS5/4+HTTP+MTProto dd/ee диалеры, Telethon-импорт, dialogs/contacts, CLI auth/chats/doctor/proxy. go.mod допинен (агент прав — модуль был без require).
- webproxy (5ed2499): полный tproxy v1 клиент — фреймы, WINDOW, bootstrap, 4 carrier, dcs.Resolver + obfuscated2/FakeTLS. Интеграция с реальным tproxy-server (сборка из master) — PASSED. 82.4% coverage.
- filters p1 (d8f4400) + p2 (4859123): Context, Compile, Pushdown, ~90 predicate-семейств, 435 тестов всего.
- Ворота репо: lint 0, build/vet/test -race зелёные.
- Волна 2: store/pace/export → scan → download+CLI wiring.
