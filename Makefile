VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X teleparse/internal/cli.version=$(VERSION)

.PHONY: build lint fmt vet test cover vuln integration check release-snapshot clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o teleparse ./cmd/teleparse

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

integration:
	go test -race -count=1 -tags webproxy_integration ./internal/webproxy/...

check: fmt lint vet test vuln

release-snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf teleparse coverage.out dist
