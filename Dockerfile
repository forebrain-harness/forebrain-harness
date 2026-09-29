# syntax=docker/dockerfile:1.7

FROM node:26-bookworm AS frontend
WORKDIR /repo
COPY ai-elements-vue ./ai-elements-vue
COPY frontend ./frontend
WORKDIR /repo/frontend
RUN corepack enable && corepack prepare pnpm@10.24.0 --activate
RUN pnpm install --frozen-lockfile
RUN pnpm run build

FROM golang:1.26-bookworm AS gobuild
ARG TARGETARCH
ARG FOREBRAIN_VERSION=
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates gcc libc6-dev libsqlite3-dev && rm -rf /var/lib/apt/lists/* \
  && mkdir -p /out
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the freshly built frontend (vite outputs to pkg/gateway/dist) so the
# binary serves the SPA with no external files.
COPY --from=frontend /repo/pkg/gateway/dist ./pkg/gateway/dist
RUN CGO_ENABLED=1 go build -trimpath -tags fts5 -ldflags="-s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=${FOREBRAIN_VERSION}" -o /out/forebrain ./cmd/forebrain
# The word-segmentation dictionary is read from beside the binary, not embedded.
RUN scripts/install-dictionary.sh /out

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=gobuild /out/forebrain /app/forebrain
COPY --from=gobuild /out/dict /app/dict
USER 65534:65534
EXPOSE 6060
ENTRYPOINT ["/app/forebrain"]
CMD ["gateway", "start"]
