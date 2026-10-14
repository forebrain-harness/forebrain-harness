# Forebrain Harness build/release chain.
#
#   make ui       build the Vue frontend into the Go embed package
#   make build    build the forebrain binary (UI embedded), then install its dictionary and models.json beside it
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
# The model catalog (models.json) is likewise installed beside the binary.
# The tokenizers static library the binary links is fetched into
# third_party/libtokenizers/ by scripts/fetch-libtokenizers.sh (see
# pkg/llm/hftok); the `libtokenizers` target runs it.
#
# The PDFium native library pkg/pdf links is fetched into
# third_party/pdfium/<goos>_<goarch>/ by scripts/fetch-pdfium.sh, which also
# generates a pdfium.pc beside it; the `pdfium` target runs it. go-pdfium's C
# files link through `// #cgo pkg-config: pdfium`, so the only wiring is
# PKG_CONFIG_PATH pointing at that directory (exported below) — pkg-config
# itself must be installed. CGO_CFLAGS/CGO_LDFLAGS are exported as well for
# environments where a pkg-config shim is unreliable.
#
# The CJK fallback fonts PDFium needs for an unembedded standard CJK font are
# fetched into assets/fonts/ by scripts/fetch-pdfium-fonts.sh (the `pdfium-fonts`
# target). install-assets.sh ships them beside the binary as the "fonts" asset
# and the runtime seeds them into $FOREBRAIN_HOME/fonts at startup.

VERSION      := $(shell cat VERSION)
LDFLAGS      := -s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=v$(VERSION)
# fts5 compiles SQLite's full-text index into the driver. Memory search
# declares an FTS5 table at schema time, so a binary built without this tag
# fails to open the state database at all.
GOFLAGS      := -trimpath -tags fts5
BIN          := build/bin/forebrain
WEBUI_DIST   := pkg/gateway/dist
# PDFium is per-target; use the host triple. pkg-config finds pdfium.pc here.
PDFIUM_TRIPLE := $(shell go env GOOS)_$(shell go env GOARCH)
PDFIUM_DIR    := $(CURDIR)/third_party/pdfium/$(PDFIUM_TRIPLE)
export PKG_CONFIG_PATH := $(PDFIUM_DIR):$(PKG_CONFIG_PATH)
export CGO_CFLAGS := -I$(PDFIUM_DIR)/include $(CGO_CFLAGS)
export CGO_LDFLAGS := -L$(PDFIUM_DIR)/lib -lpdfium $(CGO_LDFLAGS)

.PHONY: all ui libtokenizers pdfium pdfium-fonts build test hooks docker release clean

all: build

## ui: build the frontend straight into the embed package (vite outDir).
## Installs deps only on a fresh checkout; otherwise builds with what's present.
ui:
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cd frontend && [ -d node_modules ] || CI=true corepack pnpm install --frozen-lockfile
	cd frontend && corepack pnpm build

## libtokenizers: fetch the tokenizers static library the binary links
## (scripts/fetch-libtokenizers.sh; a no-op when it is already at the go.mod
## version, so this is cheap on every build and test).
libtokenizers:
	scripts/fetch-libtokenizers.sh

## pdfium: fetch the PDFium native library pkg/pdf links
## (scripts/fetch-pdfium.sh; a no-op when the host triple is already at the
## pinned release, so this is cheap on every build and test).
pdfium:
	scripts/fetch-pdfium.sh $(PDFIUM_TRIPLE)

## pdfium-fonts: fetch the Noto Sans SC/TC fallback fonts PDFium renders an
## unembedded standard CJK font (STSong-Light, SimSun, ...) with
## (scripts/fetch-pdfium-fonts.sh; a no-op when the pinned files are present).
## They ride beside the binary as the "fonts" asset (llm.assetEntries) and
## pkg/process seeds them into the home at startup.
pdfium-fonts:
	scripts/fetch-pdfium-fonts.sh

## build: compile the binary with the embedded UI, then install the dictionary
## and models.json beside it (build/bin/dict, build/bin/models.json)
## CGO is mandatory on every platform (Windows included): it pulls in the
## weixin silk voice decoder, sqlite, and other cgo-backed features.
build: ui libtokenizers pdfium pdfium-fonts
	CGO_ENABLED=1 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/forebrain
	scripts/install-dictionary.sh $(dir $(BIN))
	scripts/install-assets.sh $(dir $(BIN))
	@echo "built $(BIN) ($(VERSION)) with embedded UI, its dictionary and models.json"

## test: run the Go test suite (CGO on so silk_cgo.go is exercised; fts5 so
## the memory-search FTS5 tables exist)
test: libtokenizers pdfium pdfium-fonts
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
