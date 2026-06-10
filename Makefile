# Makefile for caretd
#
# Targets:
#   build        — compile all packages
#   test         — run all tests with race detector
#   vet          — run go vet
#   fmt          — format all Go source files in-place
#   run          — build and run the daemon (requires CARETD_DSN to be set)
#   migrate-up   — apply all pending migrations
#   migrate-down — roll back all applied migrations

.PHONY: build test vet fmt run migrate-up migrate-down

# Default DSN uses the local Unix socket.
# Fedora/RHEL: host=/tmp; Debian/Ubuntu: host=/var/run/postgresql
# Override: make run CARETD_DSN="postgres://user:pass@localhost/caretd"
CARETD_DSN ?= postgres:///caretd?host=/tmp

build:
	go build ./...

test:
	go test ./... -race -count=1

vet:
	go vet ./...

fmt:
	gofmt -w .

run: build
	CARETD_DSN=$(CARETD_DSN) go run ./cmd/caretd

migrate-up: build
	CARETD_DSN=$(CARETD_DSN) go run ./cmd/migrate up

migrate-down: build
	CARETD_DSN=$(CARETD_DSN) go run ./cmd/migrate down
