# Contributing to teleparse

Thanks for your interest in improving teleparse.

## Development setup

- Go **1.25+** (the repo tracks the current stable toolchain; see `go.mod`).
- [`golangci-lint`](https://golangci-lint.run) v2 — the repo runs a strict
  ~70-linter configuration and requires **0 findings**.
- [`gofumpt`](https://github.com/mvdan/gofumpt) — stricter formatting
  (enforced through `golangci-lint fmt`).

```bash
git clone <repo-url> && cd teleparse
go build ./...
make check   # fmt + lint + vet + test + vuln
```

## The gate

Every change must pass, with zero warnings:

```bash
make build    # CGO_ENABLED=0 static build, version stamped from git
make fmt      # golangci-lint fmt
make lint     # golangci-lint run (strict, 0 findings)
go vet ./...
make test     # go test -race -count=1 ./...
make vuln     # govulncheck ./...
```

The WEB-proxy client ships integration tests that run against the reference
`tproxy-server` and need Docker; they are excluded from the default run:

```bash
make integration   # go test -race -tags webproxy_integration ./internal/webproxy/...
```

CI (`.github/workflows/ci.yml`) runs the same gates on every push and pull
request, including the integration suite.

## Commit style

[Conventional Commits](https://www.conventionalcommits.org/) — `feat:`,
`fix:`, `docs:`, `chore:`, `refactor:`, `test:`, `ci:`, optionally scoped
(`feat(webproxy): …`). Release notes are generated from these subjects.

## Pull requests

- `golangci-lint run` clean (0 findings) and all tests green, including
  `-race`.
- New behavior comes with tests; new flags come with a config-key twin and
  README documentation.
- Keep diffs focused; one logical change per PR.

## Reporting issues

Include the `teleparse doctor` output (redact credentials) and the exact
command line that failed.
