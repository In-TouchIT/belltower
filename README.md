# Belltower - Status Page Monitor

Belltower aggregates vendor status pages into a single dashboard and API, answering "is this us, or is <vendor> down?" in one place.

## Architecture

Belltower runs as a single Go binary that periodically polls vendor status endpoints and persists the results to a SQLite database. It exposes:

- A web dashboard for human operators
- A REST API for programmatic access
- A Prometheus metrics endpoint for monitoring
- An OpenAPI specification for MCP/CLI generation

### Coverage

`providers.yaml` currently tracks 222 providers. Of those, 164 have an automatable
status endpoint; the remaining 58 are marked `adapter: manual` and appear in the
dashboard's "Manual Checks Required" section with a link to their status page.
Providers that share an endpoint are fetched once per cycle, but each records its
own incidents and components.

### Reporting honesty

A provider Belltower cannot reach is reported as `unknown`, never as operational.
The dashboard drives its per-provider status from the `indicator` field alone;
the separate `ok` / `reachable` field means only "the poll got a response" and
must not be read as a health signal.

## Quick Start

### Build

```bash
make build
```

### Configuration

Configuration is via command-line flags or environment variables. Flag names use
dashes; the matching environment variable is the uppercased name with underscores,
prefixed `BELLTOWER_`.

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--db-path` | `BELLTOWER_DB_PATH` | `./data/belltower.db` | SQLite database path |
| `--providers-file` | `BELLTOWER_PROVIDERS_FILE` | `./providers.yaml` | Provider configuration |
| `--addr` | `BELLTOWER_ADDR` | `:8088` | Address to listen on |
| `--poll-interval` | `BELLTOWER_POLL_INTERVAL` | `10m` | Polling interval |
| `--poll-timeout` | `BELLTOWER_POLL_TIMEOUT` | `10s` | Per-request timeout |
| `--concurrency` | `BELLTOWER_CONCURRENCY` | `20` | Concurrent polling workers |
| `--user-agent` | `BELLTOWER_USER_AGENT` | `belltower/1.0 …` | User-Agent for outgoing requests |

Setting `enabled: false` on a provider in `providers.yaml` takes effect on the
next start: the provider is still recorded, but is no longer polled.

### Docker

```bash
docker build -t belltower .
docker run -p 8088:8088 -v ./data:/data belltower
```

Or with Docker Compose:

```bash
docker compose up -d --build
```

The image runs on `scratch` and has no shell, so its `HEALTHCHECK` invokes the
binary's own `healthcheck` subcommand rather than `curl`. That subcommand is also
usable directly:

```bash
belltower healthcheck --addr 127.0.0.1:8088
```

## API

| Endpoint | Description |
|----------|-------------|
| `GET /api/v1/snapshot` | Full precomputed state; supports `ETag` / `If-None-Match` and gzip |
| `GET /api/v1/outages` | Providers whose latest check is non-operational (`?within=30m`, `?category=`) |
| `GET /api/v1/providers` | Provider inventory (`?adapter=`, `?category=`, `?enabled_only=true`) |
| `GET /api/v1/providers/{id}` | One provider with incidents, components, and check history |
| `GET /api/v1/incidents` | Incidents (`?q=` full-text, `?impact=`, `?status=`, `?since=`, `?open=true`, `?limit=`) |
| `GET /api/v1/changes` | Indicator transitions since a timestamp (`?since=`) |
| `GET /api/v1/health` | Service health; 503 when the snapshot is stale or missing |
| `GET /metrics` | Prometheus metrics |
| `GET /openapi.json` | OpenAPI specification |

## Deployment

See `PLAN.md` for the full deployment architecture.

Deploy to Wharf (host, user, and path are overridable):

```bash
make deploy
make deploy DEPLOY_HOST=10.0.0.5 DEPLOY_PATH=/srv/belltower
```

## Development

```bash
# Generate providers.yaml from the master CSV
make seed

# Run tests
make test

# Vet + formatting check
make lint

# Run the service locally
make run
```

### Adding an adapter

Implement `adapters.Adapter` (a single `Fetch` method), register it in
`adapters.NewRegistry`, and reference its name from `providers.yaml`. Use the
shared `httpGet` helper so the adapter gets consistent header handling,
compression, body limits, and a real HTTP status code on the `Result`. Map
provider-specific incident states through `NormalizeIncidentStatus` and parse
timestamps with `parseTime`, which returns the zero time (not "now") for values
it cannot parse.

## License

Proprietary - In Touch IT
