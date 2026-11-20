BINARY  := meshtastik
PKG     := ./cmd/meshtastik
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X meshtastik/internal/cli.version=$(VERSION)
GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@v2.20.3

# Linux and Windows build without cgo; macOS needs cgo (BLE stack and
# serial-port detection), so darwin targets build natively on a Mac.
CROSS := linux/amd64 linux/arm64 windows/amd64

.PHONY: all test race vet check build build-all release-check snapshot clean

all: check build

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

check: vet race

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

build-all: $(CROSS) darwin/native

$(CROSS):
	CGO_ENABLED=0 GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) \
		go build -ldflags '$(LDFLAGS)' \
		-o $(DIST)/$(BINARY)-$(subst /,-,$@)$(if $(findstring windows,$@),.exe) $(PKG)

darwin/native:
	@if [ "$$(go env GOOS)" = darwin ]; then \
		go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY)-darwin-$$(go env GOARCH) $(PKG); \
	else \
		echo "skipping darwin: build it on a Mac (needs cgo)"; \
	fi

# Validate .goreleaser.yaml.
release-check:
	$(GORELEASER) check

# Full local dry run of a release into dist/: every archive plus the Homebrew
# cask (dist/homebrew/Casks/meshtastik.rb). Publishes nothing.
snapshot:
	HOMEBREW_TAP_GITHUB_TOKEN=unused $(GORELEASER) release --snapshot --clean

clean:
	rm -rf $(BINARY) $(DIST)
