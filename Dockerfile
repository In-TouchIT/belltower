# Multi-stage build for belltower
# Stage 1: Build
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Cache layer for dependencies
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/belltower \
    ./cmd/belltower

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/seed \
    ./cmd/seed

# Stage 2: Production (scratch image)
FROM scratch

# CA certificates for HTTPS to the vendor status endpoints
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/belltower /belltower
COPY --from=builder /out/seed /seed
COPY providers.yaml /providers.yaml

VOLUME ["/data"]
EXPOSE 8088

# scratch has no shell and no curl, so the health check runs the binary's own
# healthcheck subcommand rather than a shell pipeline.
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=60s \
    CMD ["/belltower", "healthcheck", "--addr", "127.0.0.1:8088"]

# Runs as root by design: /data is a bind mount in the documented deployment,
# and pinning a uid here fails on any host whose data directory is owned by
# someone else. To run unprivileged, chown the data directory to the uid you
# want and set `user:` in docker-compose.yml.

ENTRYPOINT ["/belltower"]
CMD ["--db-path", "/data/belltower.db", "--providers-file", "/providers.yaml", "--addr", ":8088"]
