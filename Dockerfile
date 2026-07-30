# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Cache dependency downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/unifi-kuma \
    ./cmd/unifi-kuma

# ── test stage (used by CI and `make test-docker`) ────────────────────────────
FROM builder AS tester
RUN go test -race -cover ./...

# ── final stage ───────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot AS final

LABEL org.opencontainers.image.source="https://github.com/johntdyer/unifi-kuma"
LABEL org.opencontainers.image.description="Sync UniFi tags to Uptime Kuma monitors"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=builder /out/unifi-kuma /unifi-kuma

ENTRYPOINT ["/unifi-kuma"]
