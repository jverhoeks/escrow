FROM golang:1.25-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -trimpath \
    -o /escrow ./cmd/escrow

# ── runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates: needed for TLS connections to upstream registries (npmjs, pypi, etc.)
# tzdata: correct timestamps in logs
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S escrow && adduser -S -G escrow escrow

COPY --from=build /escrow /usr/local/bin/escrow

# Default cache directory (mount a volume here for persistence across restarts)
RUN mkdir -p /data/escrow-cache && chown escrow:escrow /data/escrow-cache

USER escrow
WORKDIR /data

EXPOSE 8888

# Probe the health endpoint every 30s; fail after 3 consecutive misses
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8888/healthz || exit 1

ENTRYPOINT ["escrow"]
CMD ["--host=0.0.0.0"]
