# Belltower - Status Page Monitor

Belltower aggregates vendor status pages into a single dashboard and API, answering "is this us, or is <vendor> down?" in one place.

## Architecture

Belltower runs as a single Go binary that periodically polls 218+ vendor status pages and persists the results to a SQLite database. It exposes:

- A web dashboard for human operators
- A REST API for programmatic access
- A Prometheus metrics endpoint for monitoring
- An OpenAPI specification for MCP/CLI generation

## Quick Start

### Build

```bash
make build
```

### Configuration

Configuration is done via environment variables or command-line flags:

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--db-path` | `BELLTOWER_DB_PATH` | `./data/belltower.db` | SQLite database path |
| `--providers-file` | `BELLTOWER_PROVIDERS_FILE` | `./providers.yaml` | Provider configuration |
| `--addr` | `BELLTOWER_ADDR` | `:8088` | Address to listen on |
| `--poll-interval` | `BELLTOWER_POLL_INTERVAL` | `10m` | Polling interval |
| `--poll-timeout` | `BELLTOWER_POLL_TIMEOUT` | `10s` | Per-request timeout |
| `--concurrency` | `BELLTOWER_CONCURRENCY` | `20` | Concurrent polling workers |

### Docker

```bash
docker build -t belltower .
docker run -p 8088:8088 -v ./data:/data belltower
```

Or with Docker Compose:

```bash
docker compose up -d --build
```

## Deployment

See `PLAN.md` for the full deployment architecture.

Deploy to Wharf:

```bash
make deploy
```

## Development

```bash
# Generate providers.yaml from the master CSV
make seed

# Run tests
make test

# Run the service locally
make run
```

## License

Proprietary - In Touch IT
