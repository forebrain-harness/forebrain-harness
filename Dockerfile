# syntax=docker/dockerfile:1.7
#
# The syntax line pins the frontend, so a build network that cannot reach
# docker.io resolves it from a mirror instead:
#   docker build --build-arg BUILDKIT_SYNTAX=docker.m.daocloud.io/docker/dockerfile:1.7 .
# (or point `registry-mirrors` at one, in which case the line above is enough).
#
# The forebrain gateway image.
#
# Runtime base: debian:bookworm-slim, not ubuntu:24.04 (plan 214 §3.1). The
# frontend stage is bookworm already, so the Node.js 22 this image carries is
# copied out of that stage — same distribution, same glibc and libstdc++, no
# second package source and no second download. bookworm's bubblewrap (0.7.0)
# supports every flag pkg/safety/backend_linux.go passes. And the image was
# bookworm-slim before, so the runtime's own assumptions do not move: libpdfium.so
# beside the binary on its -rpath, fontconfig for the platform fonts, non-root.
#
# What the image carries: the gateway binary with its assets, and the toolchain
# the agent's shell tool expects — git, Node.js 22 + npm, Python 3 + pip, Go
# (plan 214 §0.4). It also carries bubblewrap: a deployment that mounts
# deploy/seccomp/forebrain-bubblewrap.json on a host that allows unprivileged
# user namespaces gets the sandbox; with either missing the gateway runs exactly
# the same and every command is full access (plan 214 §0.3).
#
# Versions are the ARGs below, so a toolchain upgrade is one line (plan 214 §7).
#
# The image is built where there is a network and runs where there is none. An
# offline build points these at its own mirrors:
#   GO_VERSION / GO_DIST_URL  the official Go tarball, or an internal copy of
#                             go.dev/dl (the tarball is unpacked to /usr/local/go).
#   GOPROXY                   the Go module proxy.
#   NPM_REGISTRY              the registry pnpm resolves packages from.
#   APT_MIRROR                the Debian mirror both build stages use.
#   GITHUB_MIRROR             a prefix for GitHub release downloads. The release
#                             host redirects to objects.githubusercontent.com,
#                             which some networks block outright; a prefix such
#                             as https://ghfast.top/ serves the same bytes from
#                             a reachable host, and every download is checked
#                             against the sha256 pinned in the repo either way.
#   FOREBRAIN_VERSION         stamped into the binary's version
#                             (`make docker` passes v$(VERSION)).
#
# The defaults are the public origins. They are overridable rather than fixed
# because a build network's reachability is a property of the network, not of
# this repository — and because a build that hangs on an unreachable host looks
# like a slow build instead of a failed one.
ARG NODE_IMAGE=node:22-bookworm
ARG GO_BUILD_IMAGE=golang:1.27-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
ARG GO_VERSION=1.26.9
ARG GO_DIST_URL=https://go.dev/dl
ARG GOPROXY=https://proxy.golang.org,direct
ARG NPM_REGISTRY=https://registry.npmjs.org
ARG APT_MIRROR=deb.debian.org
ARG GITHUB_MIRROR=

FROM ${NODE_IMAGE} AS frontend
ARG NPM_REGISTRY
ARG APT_MIRROR
WORKDIR /repo
# VERSION is what the UI prints and what vite bakes in at build time; it is read
# from the repository root, one level above the frontend package.
COPY VERSION ./VERSION
COPY ai-elements-vue ./ai-elements-vue
COPY frontend ./frontend
WORKDIR /repo/frontend
RUN if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g; s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    fi
RUN corepack enable && corepack prepare pnpm@10.24.0 --activate
RUN pnpm config set registry "${NPM_REGISTRY}" --global
RUN pnpm install --frozen-lockfile
RUN pnpm run build

FROM ${GO_BUILD_IMAGE} AS gobuild
ARG TARGETARCH
ARG FOREBRAIN_VERSION=
ARG GOPROXY
ARG APT_MIRROR
ARG GITHUB_MIRROR
# The font fetch in this stage resolves packages from NPM_REGISTRY too; ARGs do
# not cross stages, and an undeclared one expands to empty — which silently
# falls back to the public registry the mirror was asked to replace.
ARG NPM_REGISTRY
# pkg-config is what go-pdfium's `// #cgo pkg-config: pdfium` directive calls;
# jq is what scripts/fetch-pdfium.sh uses to resolve the release asset id.
RUN if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g; s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    fi \
  && apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gcc libc6-dev libsqlite3-dev pkg-config jq && rm -rf /var/lib/apt/lists/* \
  && mkdir -p /out
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the freshly built frontend (vite outputs to pkg/gateway/dist) so the
# binary serves the SPA with no external files.
COPY --from=frontend /repo/pkg/gateway/dist ./pkg/gateway/dist
# The tokenizers static library the binary links is not committed; fetch the
# one for this image's architecture (amd64 or arm64) before building.
RUN FOREBRAIN_GITHUB_MIRROR="${GITHUB_MIRROR}" scripts/fetch-libtokenizers.sh linux/${TARGETARCH}
# PDFium (pkg/pdf) is likewise fetched, not committed; its shared library is a
# runtime dependency of the image, so it is copied into the final stage below.
# The CJK fallback fonts are fetched here too and install-assets.sh ships them
# beside the binary as the "fonts" asset, which the runtime seeds into the home.
RUN FOREBRAIN_GITHUB_MIRROR="${GITHUB_MIRROR}" scripts/fetch-pdfium.sh linux/${TARGETARCH} \
  && FOREBRAIN_NPM_REGISTRY="${NPM_REGISTRY}" scripts/fetch-pdfium-fonts.sh
# Link PDFium through the pkg-config file the fetch step generated. The rpath
# names /app, where the final stage places libpdfium.so next to the binary.
RUN PKG_CONFIG_PATH="/src/third_party/pdfium/linux_${TARGETARCH}" \
    CGO_LDFLAGS="-Wl,-rpath,/app" \
    CGO_ENABLED=1 go build -trimpath -tags fts5 -ldflags="-s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=${FOREBRAIN_VERSION}" -o /out/forebrain ./cmd/forebrain
# The word-segmentation dictionary, the model catalog and the CJK fonts are
# read from beside the binary, not embedded.
RUN scripts/install-dictionary.sh /out && scripts/install-assets.sh /out
# Stage the PDFium shared library with the rest of /out, so the final image is
# a plain copy and does not need this stage's TARGETARCH.
RUN cp "/src/third_party/pdfium/linux_${TARGETARCH}/lib/libpdfium.so" /out/libpdfium.so

FROM ${RUNTIME_IMAGE}
ARG GO_VERSION
ARG GO_DIST_URL
ARG APT_MIRROR
ENV DEBIAN_FRONTEND=noninteractive

# bubblewrap is the sandbox backend the Linux build looks for on PATH
# (pkg/safety/backend_linux.go); without a seccomp profile that lets a
# containerised process create namespaces, its probe fails and the gateway
# reports the sandbox as unavailable rather than refusing to start.
#
# libstdc++6 and libfontconfig1 are PDFium's runtime needs. curl is the
# HEALTHCHECK's client and an everyday tool. The Python packages are the shell
# tool's python3 arm; the EXTERNALLY-MANAGED marker is removed because an agent
# running `pip3 install <package>` in a container is not going to remember
# --break-system-packages (a venv from python3-venv is there too, and pip still
# falls back to a user install when the system site-packages is not writable).
RUN if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g; s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    fi \
  && apt-get update && apt-get install -y --no-install-recommends \
      bubblewrap \
      ca-certificates \
      curl \
      git \
      libfontconfig1 \
      libstdc++6 \
      python3 \
      python3-pip \
      python3-venv \
    && rm -f /usr/lib/python3.*/EXTERNALLY-MANAGED \
    && rm -rf /var/lib/apt/lists/*

# Node.js 22 and npm, from the bookworm stage that already has them: the runtime
# base is the same distribution, so this is a copy and not a second install.
# The whole /usr/local/bin is copied as a directory because the npm/npx/corepack
# launchers there are symlinks into /usr/local/lib/node_modules; a single-file
# COPY dereferences a symlink into a script that then resolves '../lib/cli.js'
# against /usr/local/bin and dies on first run.
COPY --from=frontend /usr/local/bin /usr/local/bin
COPY --from=frontend /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
COPY --from=frontend /usr/local/lib/node_modules/corepack /usr/local/lib/node_modules/corepack

# The Go toolchain, unpacked where the official tarball expects to live.
# GO_DIST_URL lets an offline build fetch the tarball from an internal mirror.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) goarch=amd64 ;; \
      arm64) goarch=arm64 ;; \
      *) echo "unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/go.tgz "${GO_DIST_URL}/go${GO_VERSION}.linux-${goarch}.tar.gz"; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm -f /tmp/go.tgz; \
    /usr/local/go/bin/go version
ENV PATH=/usr/local/go/bin:/home/forebrain/go/bin:$PATH

WORKDIR /app
COPY --from=gobuild /out/forebrain /app/forebrain
COPY --from=gobuild /out/dict /app/dict
COPY --from=gobuild /out/models.json /app/models.json
COPY --from=gobuild /out/tokenizers /app/tokenizers
COPY --from=gobuild /out/fonts /app/fonts
# The PDFium shared library is a runtime dependency: it sits next to the binary,
# which is linked with -rpath /app so the loader finds it here.
COPY --from=gobuild /out/libpdfium.so /app/libpdfium.so

# The first-start configuration writer (plan 214 §3.1): it runs only for
# `gateway start`, and never overwrites a forebrain.yaml that is already there —
# the web's configuration editor owns that file after the first boot.
COPY deploy/docker/entrypoint.sh /usr/local/bin/forebrain-entrypoint
RUN chmod 0755 /usr/local/bin/forebrain-entrypoint

# One fixed, unprivileged identity. Its numeric UID is what a bind mount's owner
# has to match, so it is documented and never changes.
RUN groupadd --gid 10001 forebrain \
    && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/forebrain --shell /bin/bash forebrain \
    && mkdir -p /data/forebrain \
    && chown forebrain:forebrain /data/forebrain \
    # `npm install -g` is a thing an agent runs; its global prefix must be
    # writable by that agent, not only by root.
    && chown -R forebrain:forebrain /usr/local/lib/node_modules /usr/local/bin

ENV HOME=/home/forebrain \
    FOREBRAIN_HOME=/data/forebrain

USER 10001:10001
# The durable state: the whole forebrain home (config, .env, SQLite state,
# workspace, logs). Mount it, or the install starts over with the container.
VOLUME ["/data/forebrain"]
EXPOSE 6060

# /healthz is the gateway's liveness and readiness endpoint on the same
# listener (pkg/gateway/server.go). The address is the one the entrypoint wrote
# into forebrain.yaml, so an install that changed the port sets
# FOREBRAIN_HTTP_ADDR and this follows. curl accepts 0.0.0.0 as loopback.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD ["/bin/sh", "-c", "curl -fsS -o /dev/null \"http://${FOREBRAIN_HTTP_ADDR:-127.0.0.1:6060}/healthz\""]

ENTRYPOINT ["/usr/local/bin/forebrain-entrypoint"]
CMD ["gateway", "start"]
