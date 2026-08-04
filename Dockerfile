# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
# Always build on the runner's native platform (BUILDPLATFORM), never the
# target platform — Go cross-compiles natively for any GOOS/GOARCH from a
# single toolchain, so there's no need to run the compile itself through
# QEMU emulation for arm64/arm/v7 the way a naive multi-platform build
# would. Only the final COPY (a static binary, no computation) happens per
# target platform.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /src

# Cache dependency downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} go build \
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

EXPOSE 9090

# The distroless base image has no shell, curl, or wget, so a normal
# CMD-based HEALTHCHECK can't run one — instead this invokes the app's own
# binary with -healthcheck, which just does an HTTP GET against its own
# /healthz and exits 0/1 accordingly. No-op (always exits 0) if HTTP_ADDR
# has been set to "" to disable the HTTP server entirely.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/unifi-kuma", "-healthcheck"]

ENTRYPOINT ["/unifi-kuma"]
