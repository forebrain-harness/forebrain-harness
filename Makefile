# Forebrain Harness build/release chain.
#
#   make ui       build the Vue frontend into the Go embed package
#   make build    build the forebrain binary (UI embedded) and install its dictionary beside it
#   make test     run the Go test suite
#   make hooks    install the repository git hooks (commit title + DCO sign-off)
#   make docker   build the container image
#   make release  build per-platform npm packages (the script rebuilds the UI
#                 first; it is embedded in each binary)
#   make clean    remove build artifacts
#
# The frontend is embedded via go:embed (pkg/gateway). `make build` always
# rebuilds the UI first so the binary serves the current frontend.
#
# The word-segmentation dictionary memory search reads is not embedded: it is
# installed into dict/ beside the binary, which is where forebrain looks for it.

VERSION      := $(shell cat VERSION)
LDFLAGS      := -s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=v$(VERSION)
# fts5 compiles SQLite's full-text index into the driver. Memory search
# declares an FTS5 table at schema time, so a binary built without this tag
# fails to open the state database at all.
GOFLAGS      := -trimpath -tags fts5
BIN          := build/bin/forebrain
WEBUI_DIST   := pkg/gateway/dist

.PHONY: all ui build test hooks docker release clean

all: build

## ui: build the frontend straight into the embed package (vite outDir).
## Installs deps only on a fresh checkout; otherwise builds with what's present.
ui:
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cd frontend && [ -d node_modules ] || CI=true corepack pnpm install --frozen-lockfile
	cd frontend && corepack pnpm build

## build: compile the binary with the embedded UI, then install the dictionary
## beside it (build/bin/dict)
## CGO is mandatory on every platform (Windows included): it pulls in the
## weixin silk voice decoder, sqlite, and other cgo-backed features.
build: ui
	CGO_ENABLED=1 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/forebrain
	scripts/install-dictionary.sh $(dir $(BIN))
	@echo "built $(BIN) ($(VERSION)) with embedded UI and its dictionary"

## test: run the Go test suite (CGO on so silk_cgo.go is exercised; fts5 so
## the memory-search FTS5 tables exist)
test:
	CGO_ENABLED=1 go test -tags fts5 ./... -count=1

## hooks: point git at this repository's hooks (commit title check + DCO
## sign-off). A developer choice: installing them equals agreeing to the DCO.
hooks:
	git config core.hooksPath .githooks

## docker: build the container image (frontend built inside the image)
docker:
	docker build --build-arg FOREBRAIN_VERSION=v$(VERSION) -t forebrain:$(VERSION) .

## release: build per-platform npm packages. The script rebuilds the web UI
## first (make ui) so every binary embeds the current frontend.
release:
	FOREBRAIN_VERSION=$(VERSION) npm/scripts/build-platform-packages.sh

## clean: drop build outputs. pkg/gateway/dist is committed source now; `make
## ui` rebuilds it, so clean must not delete it.
clean:
	rm -rf build/bin
