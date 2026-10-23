# syntax=docker/dockerfile:1

# Build context is the repository root, not daemon/: daemon/go.mod replaces
# ajsproxy with ../emulator, so both module trees must be present.
#
#   docker build -f daemon/Dockerfile -t ajs-accountd:local .

FROM golang:1.26-alpine AS build

ENV GOTOOLCHAIN=local
WORKDIR /src

# Module manifests first so a source-only change reuses the build cache.
COPY daemon/go.mod ./daemon/go.mod
COPY emulator/go.mod ./emulator/go.mod

COPY daemon ./daemon
COPY emulator ./emulator

WORKDIR /src/daemon
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /out/ajs-accountd ./cmd/ajs-accountd

FROM alpine:3.22 AS runtime

# ca-certificates: the optional vendor login path and any TLS upstream use the
# system roots. The daemon itself needs no shell tooling beyond the healthcheck.
RUN apk add --no-cache ca-certificates \
 && addgroup -S ajs \
 && adduser -S -D -H -G ajs -h /data ajs \
 && mkdir -p /data \
 && chown ajs:ajs /data

COPY --from=build /out/ajs-accountd /usr/local/bin/ajs-accountd

# Loopback by default: the daemon has no TLS and a bearer token baked into the
# dashboard bundle, so it must not listen on a routable address.
ENV AJS_LISTEN=127.0.0.1:2261 \
    AJS_STATE_DIR=/data

VOLUME ["/data"]
USER ajs
EXPOSE 2261

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:2261/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/ajs-accountd"]
