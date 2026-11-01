VERSION ?= dev
LDFLAGS := -s -w -X github.com/dmtrkzntsv/ship/internal/cli.Version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64

.PHONY: build test cover lint dist e2e clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ship ./cmd/ship

test:
	go test ./...

# Unit-test coverage of internal/... must stay at or above COVER_MIN percent.
# cmd/ship is a one-line main and test/e2e holds the e2e helpers.
COVER_MIN ?= 90
cover:
	go test -coverpkg=./internal/... -coverprofile=coverage.out ./internal/...
	@go tool cover -func=coverage.out | awk -v min=$(COVER_MIN) '/^total:/ { \
		sub("%", "", $$3); printf "coverage %s%% (minimum %s%%)\n", $$3, min; \
		if ($$3 + 0 < min) { print "coverage below minimum"; exit 1 } }'

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

dist:
	rm -rf dist
	mkdir -p dist
	@set -e; for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "build $$os/$$arch"; \
		extra=""; [ "$$arch" = arm ] && extra="GOARM=6"; \
		env CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $$extra go build -ldflags "$(LDFLAGS)" -o dist/ship-$$os-$$arch ./cmd/ship; \
		tmp=$$(mktemp -d); cp dist/ship-$$os-$$arch $$tmp/ship; \
		tar -C $$tmp -czf dist/ship-$(VERSION)-$$os-$$arch.tar.gz ship; \
		rm -rf $$tmp; \
	done
	cd dist && sha256sum ship-* > SHA256SUMS

# Needs root and systemd; see test/e2e/e2e.sh.
e2e: build
	test/e2e/e2e.sh

clean:
	rm -rf bin dist coverage.out
