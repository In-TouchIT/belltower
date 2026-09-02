# Multi-stage build for belltower (formerly statusd)
# Stage 1: Build
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Cache layer for dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build the binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o bin/belltower \
    cmd/belltower/main.go

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -o bin/seed \
    cmd/seed/main.go

# Stage 2: Production (scratch image)
FROM scratch

# Copy CA certificates for HTTPS
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the built binaries
COPY --from=builder /app/bin/belltower /belltower
COPY --from=builder /app/bin/seed /seed

# Copy providers configuration
COPY providers.yaml /providers.yaml

# Create data directory (bind mounted at runtime)
VOLUME ["/data"]

# Expose the application port
EXPOSE 8088

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
  CMD ["/belltower", "--health-check"] || exit 1

# Run the application
ENTRYPOINT ["/belltower"]
CMD ["--db-path", "/data/belltower.db", "--providers-file", "/providers.yaml", "--addr", ":8088"]
