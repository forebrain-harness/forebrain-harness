# syntax=docker/dockerfile:1.7

FROM node:22-bookworm AS frontend
WORKDIR /repo
COPY ai-elements-vue ./ai-elements-vue
COPY frontend ./frontend
WORKDIR /repo/frontend
RUN corepack enable && corepack prepare pnpm@10.24.0 --activate
RUN pnpm install --frozen-lockfile
RUN pnpm run build

FROM golang:1.27-bookworm AS gobuild
ARG TARGETARCH
ARG FOREBRAIN_VERSION=
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gcc libc6-dev libsqlite3-dev && rm -rf /var/lib/apt/lists/* \
  && mkdir -p /out
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the freshly built frontend (vite outputs to pkg/gateway/dist) so the
# binary serves the SPA with no external files.
COPY --from=frontend /repo/pkg/gateway/dist ./pkg/gateway/dist
# The tokenizers static library the binary links is not committed; fetch the
# one for this image's architecture (amd64 or arm64) before building.
RUN scripts/fetch-libtokenizers.sh linux/${TARGETARCH}
RUN CGO_ENABLED=1 go build -trimpath -tags fts5 -ldflags="-s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=${FOREBRAIN_VERSION}" -o /out/forebrain ./cmd/forebrain
# The word-segmentation dictionary and the model catalog are read from beside
# the binary, not embedded.
RUN scripts/install-dictionary.sh /out && scripts/install-assets.sh /out

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=gobuild /out/forebrain /app/forebrain
COPY --from=gobuild /out/dict /app/dict
COPY --from=gobuild /out/models.json /app/models.json
COPY --from=gobuild /out/tokenizers /app/tokenizers
USER 65534:65534
EXPOSE 6060
ENTRYPOINT ["/app/forebrain"]
CMD ["gateway", "start"]
