GO ?= go
VERSION ?= 0.1.0
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build test race vet

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/channel-owner ./cmd/channel-owner
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/fx-links ./cmd/fx-links

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...
