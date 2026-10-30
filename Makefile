VERSION ?= dev
LDFLAGS := -s -w -X github.com/dmtrkzntsv/ship/internal/cli.Version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64

.PHONY: build test lint dist e2e clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ship ./cmd/ship

test:
	go test ./...

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

e2e:
	test/e2e/e2e.sh

clean:
	rm -rf bin dist
