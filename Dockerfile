# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
FROM golang:1.24-alpine AS build

WORKDIR /src

# Dependencies first so the module cache survives source edits.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/shuaiZend/mage-mediagc/internal/version.Version=${VERSION} \
        -X github.com/shuaiZend/mage-mediagc/internal/version.Commit=${COMMIT} \
        -X github.com/shuaiZend/mage-mediagc/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/mage-mediagc ./cmd/mage-mediagc

# ---------------------------------------------------------------------------
# Runtime stage
# ---------------------------------------------------------------------------
FROM alpine:3.20

# ca-certificates and tzdata are not needed by the tool itself, but they make
# the image usable for shelling into when a run needs investigating.
RUN apk add --no-cache ca-certificates tzdata

COPY --from=build /out/mage-mediagc /usr/local/bin/mage-mediagc

LABEL org.opencontainers.image.title="mage-mediagc" \
      org.opencontainers.image.description="Standalone, reversible garbage collector for Magento 2 catalog media" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/shuaiZend/mage-mediagc"

# The tool's whole job is renaming files that belong to the web server and
# talking to MySQL, so it runs as root by default. Pass `--user` to run as the
# shop's own uid/gid instead:
#
#   docker run --rm -v /data/wwwroot/shop:/magento \
#     --user "$(id -u www-data):$(id -g www-data)" \
#     ghcr.io/shuaiZend/mage-mediagc scan --magento-root /magento
WORKDIR /magento

ENTRYPOINT ["/usr/local/bin/mage-mediagc"]
CMD ["--help"]
