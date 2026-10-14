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
# pkg-config is what go-pdfium's `// #cgo pkg-config: pdfium` directive calls;
# jq is what scripts/fetch-pdfium.sh uses to resolve the release asset id.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gcc libc6-dev libsqlite3-dev pkg-config jq && rm -rf /var/lib/apt/lists/* \
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
# PDFium (pkg/pdf) is likewise fetched, not committed; its shared library is a
# runtime dependency of the image, so it is copied into the final stage below.
# The CJK fallback fonts are fetched here too and install-assets.sh ships them
# beside the binary as the "fonts" asset, which the runtime seeds into the home.
RUN scripts/fetch-pdfium.sh linux/${TARGETARCH} && scripts/fetch-pdfium-fonts.sh
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

FROM debian:bookworm-slim
# PDFium is a C++ shared library, and on Linux it reaches fontconfig for the
# platform fonts FontDirs adds alongside this repository's own.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 libfontconfig1 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=gobuild /out/forebrain /app/forebrain
COPY --from=gobuild /out/dict /app/dict
COPY --from=gobuild /out/models.json /app/models.json
COPY --from=gobuild /out/tokenizers /app/tokenizers
COPY --from=gobuild /out/fonts /app/fonts
# The PDFium shared library is a runtime dependency: it sits next to the binary,
# which is linked with -rpath /app so the loader finds it here.
COPY --from=gobuild /out/libpdfium.so /app/libpdfium.so
USER 65534:65534
EXPOSE 6060
ENTRYPOINT ["/app/forebrain"]
CMD ["gateway", "start"]
