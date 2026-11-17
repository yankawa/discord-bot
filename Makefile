GO ?= go
VERSION ?= 0.1.0
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build build-times build-utils test race vet

build: build-times build-utils

build-times:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/times-bot ./cmd/times-bot

build-utils:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/utils-bot ./cmd/utils-bot

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...
