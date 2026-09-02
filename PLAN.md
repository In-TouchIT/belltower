# Status Page Monitor — `belltower` on Wharf + printing-press MCP

## Context

We maintain one consolidated CSV of vendor status pages,
`/home/ccarson/Projects-dev/status-page/Status Pages Master.csv` — **224 providers across
33 categories** — that today is just a bookmark list. When tickets spike, answering "is this us, or is Cloudflare/Todyl/HaloPSA
down?" means an engineer opening tabs by hand.

We want that answered in one place, two ways:

1. **A dashboard** an engineer can glance at on the LAN.
2. **An MCP** so Claude/Hermes/Aegis can ask "what's down right now" and "search
   incidents for X" during triage.

The decided architecture separates the two: a container on Wharf polls every 5–15 min and
persists results; the MCP is a thin reader over a precomputed JSON snapshot. The MCP never
blocks on 218 HTTP requests, never rate-limits a vendor, and stays fast regardless of how
many agents query it.

**Decisions already made** (confirmed with the user):

| | |
|---|---|
| Stack | Go single binary, multi-stage → `scratch` image (~20 MB) |
| MCP | Container serves `/openapi.json`; `/printing-press` generates `statuspage-pp-cli` + `statuspage-pp-mcp` |
| Exposure | LAN-only `http://192.168.111.122:8088` — no nginx change |
| Alerting | `/metrics` for the existing Steward Prometheus; no ticket automation yet |

## Verified findings (probed live, 2026-09-02 — do not re-derive)

**Adapter coverage — 150 of 218 unique pages (69%) need no per-provider code:**

| Adapter | Count | Endpoint pattern | Verified against |
|---|---|---|---|
| `statuspage` | **128** | `<base>/api/v2/summary.json` | Cloudflare 200, 211 KB |
| `statusio` | 6 | `https://api.status.io/1.0/status/<pageID>` | all 6 resolved, 200 |
| `betterstack` | 3 | `<base>/index.json` | cloudradial, level.io, quad9 |
| `sorryapp` | 3 | `<base>/api/v1/status` | broadcom, pingdom, postmark |
| `instatus` | 3 | `<base>/summary.json` | 3 hits |
| Hand-rolled majors | 7 | see below | all 200 |
| **Needs work / skip** | ~68 | HTML-only or login-walled | Okta 401, Zendesk 404, Fastly 403, Zscaler 404, Acronis + CrowdStrike portals, IRS, Xfinity, Apple, T-Mobile, Cisco |

**status.io page IDs (already resolved — hardcode these, don't re-scrape):**

```
GitLab       5b36dc6502d06804c08349f7    Mimecast      5d849b1c02e65b3ec45369d4
ConnectWise  619cf82551fec9053d612f09    Let's Encrypt 55957a99e800baa4470002da
HaloPSA      63ef45da7ee94905308a1a4a    Hornet        591aaa7fe69f388425000fda
```

**Hand-rolled majors (all returned 200):**

```
GCP        https://status.cloud.google.com/incidents.json
AWS        https://health.aws.amazon.com/public/currentevents
Slack      https://slack-status.com/api/v2.0.0/current
Heroku     https://status.heroku.com/api/v4/current-status
Salesforce https://api.status.salesforce.com/v1/incidents
Azure      https://azurestatuscdn.azureedge.net/en-us/status/feed/   (RSS, not JSON)
GWorkspace https://www.google.com/appsstatus/dashboard/incidents.json
M365       Graph serviceHealth — needs tenant auth, OUT OF SCOPE for v1
```

**Wharf (192.168.111.122)** — `ubuntu@` + passwordless sudo (**not** `ccarson`); `docker`
requires sudo. 16 GB RAM (6 used), 224 GB disk (40%), up 54 days. Docker 29.6.1, Compose
v5.3.1. Clean egress verified to all four probe targets **and** `proxy.golang.org` (200),
so the multi-stage build works on-box; no proxy env.

**Ports in use:** 22, 80, 443 (nginx, `local-ai-app` → ai.intouchit.com), 3000
(open-webui), 5678 (n8n), 8000 (honcho-api), 8501 (m365-audit-dashboard), 9100
(node_exporter), 5432/6379 (localhost-only). → **`8088` is free; claim it.**

**Stack convention:** compose stacks live at `/opt/n8n`, `/opt/m365-audit` → use
**`/opt/status-page`**.

**Two duplicate URLs** in the CSV (`status.twilio.com`, `status.broadcom.com` — two
provider names each, e.g. SendGrid→Twilio). Fetch must be **keyed by endpoint, not
provider**, so 224 providers → 218 fetches. Provider names are unique; no name dedupe
needed.

## Architecture

```
/opt/status-page/                    (Wharf, compose-managed like /opt/n8n)
  docker-compose.yml
  providers.yaml                     generated from the master CSV, then hand-corrected
  data/status.db                     SQLite, bind-mounted (single writer)

belltower  (one Go binary, three goroutine groups)
  ├── poller   every POLL_INTERVAL (default 10m), 20-wide, per-request 10s timeout
  ├── api      /api/v1/*  +  /openapi.json  +  /metrics
  └── ui       /  server-rendered html/template + HTMX (no JS build step)
```

### The efficiency trick: a precomputed snapshot

At the **end of every poll cycle** the poller serializes the entire current world into one
JSON document and stores it in `snapshots` (plus an in-memory copy + gzip'd bytes + ETag).
`GET /api/v1/snapshot` is then a memory read — no queries, no per-request work — even with
every agent on the fleet polling it.

That single document (~60–90 KB raw, ~12 KB gzipped) is what the MCP pulls, and it answers
"what's down", "what's degraded", "how stale is this" without a second call.

### Data model (`internal/store`)

```sql
providers(id TEXT PK, name, category, page_url, adapter, endpoint, tier INT, enabled INT)
checks(endpoint, ts, http_code, latency_ms, indicator, ok, err)      -- time series
incidents(provider_id, ext_id, title, impact, status, started_at,
          resolved_at, url, body, raw_json, first_seen, last_seen)   -- UPSERT on (provider_id, ext_id)
components(provider_id, name, status, updated_at)
snapshots(cycle_id INTEGER PK, built_at, json BLOB, etag)            -- keep last ~50
incidents_fts                                                        -- FTS5 over (title, body)
```

`raw_json` on every incident means adapter bugs are re-parseable without re-polling.

`checks` retention: prune > 90 days on startup and once daily.

**On SQLite:** the n8n `SqliteWriteConnectionMutex` pain ([[n8n-halopsa-sync-pipeline]]) came
from ~6 concurrent workflow writers. Here there is exactly **one** writer goroutine and
readers are `_journal_mode=WAL&_busy_timeout=5000`. Not a risk — but keep writes batched in
one transaction per cycle.

### Adapter interface

Every adapter is the same shape, which is why 128 providers cost one implementation:

```go
type Adapter interface {
    Fetch(ctx context.Context, p Provider) (Result, error)
}
type Result struct {
    Indicator  string      // none|minor|major|critical|maintenance|unknown
    Incidents  []Incident
    Components []Component
}
```

`adapter: manual` providers are stored, rendered on the dashboard as "check by hand" with
their `page_url`, and reported in `/metrics` as unmonitored — visible gaps, not silent ones.

### API surface (this is the `/printing-press` input)

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/snapshot` | **the one the MCP uses** — full current state, ETag + gzip |
| `GET /api/v1/outages` | non-operational only, `?min_impact=&category=&tier=` |
| `GET /api/v1/providers` | inventory + adapter + last check |
| `GET /api/v1/providers/{id}` | 360: indicator, open incidents, components, uptime N days |
| `GET /api/v1/incidents` | `?q=` (FTS5) `&since=&impact=&status=&limit=` |
| `GET /api/v1/changes` | what changed since `?since=` — the standup/triage query |
| `GET /api/v1/health` | own health: last cycle, duration, adapter error counts |
| `GET /openapi.json` | hand-authored OpenAPI 3.1 → generates the CLI + MCP |
| `GET /metrics` | Prometheus |

Read-only. No auth in v1 (LAN-only, same posture as n8n `:5678` and m365-audit `:8501`).

### Metrics for Steward

```
statuspage_provider_up{provider,category,adapter}
statuspage_provider_indicator{provider,indicator}
statuspage_incident_open{provider,impact}
statuspage_fetch_age_seconds{provider}
statuspage_poll_cycle_duration_seconds
statuspage_adapter_errors_total{adapter,reason}
statuspage_providers_unmonitored          # the `manual` count
```

## Repo layout

Source of truth is this working directory, **`/home/ccarson/Projects-dev/status-page`**
(not currently a git repo — `git init` as step 0). Deployed to `/opt/status-page` on Wharf.

```
cmd/belltower/main.go            flags/env, wiring, graceful shutdown
cmd/seed/main.go               one-shot: master CSV -> providers.yaml
internal/adapters/            statuspage.go statusio.go betterstack.go sorryapp.go
                              instatus.go rss.go gcp.go aws.go slack.go heroku.go
                              salesforce.go gworkspace.go registry.go
internal/store/               schema.go (embedded DDL), queries.go, snapshot.go, prune.go
internal/poller/              poller.go (worker pool, endpoint dedup, jitter)
internal/api/                 routes.go, handlers.go, metrics.go, openapi.json (embedded)
internal/ui/                  templates/*.html, static/ (HTMX vendored, no CDN)
providers.yaml                generated, then hand-corrected
Dockerfile                     multi-stage golang:1.23 -> scratch
docker-compose.yml
Makefile                       build, test, seed, deploy
README.md
```

## Build phases

**Phase 0 — seed the inventory.** `cmd/seed` reads `Status Pages Master.csv` (the single
source of truth — the Additions CSV has been consolidated into it and is gone), groups by
endpoint so the Twilio/Broadcom pairs fetch once, and emits `providers.yaml` with `adapter`
pre-assigned from the probe results above (`statuspage` default; the 21 vendor-specific and
7 hand-rolled entries written explicitly; the ~68 unresolved as `manual`). The 4 rows with
no page URL — **Ramp, Hudu, IT Glue, Blackpoint Cyber** — become `enabled: false`.
**Checkpoint: review `providers.yaml` before writing any adapter.**

**Phase 1 — store + poller + `statuspage` adapter.** Gets 128 providers live. Prove the
snapshot builder and `/api/v1/snapshot` end to end.

**Phase 2 — remaining adapters.** `statusio` (IDs above), `betterstack`, `sorryapp`,
`instatus`, then the 7 majors (Azure via the RSS adapter). → 150 live.

**Phase 3 — dashboard.** Single page: category-grouped grid, red/amber/green per provider,
open incidents inline, "stale >2 cycles" badge, `?category=` filter, HTMX auto-refresh
every 60 s. `manual` providers in a separate "check by hand" section.

**Phase 4 — containerize + deploy.** `Dockerfile` (multi-stage → `scratch`, `CGO_ENABLED=0`,
`modernc.org/sqlite` so no libc needed), compose with `restart: unless-stopped`,
`mem_limit: 512m` (m365-audit/n8n have no limits and n8n OOM-crashes because of it — don't
repeat that), bind-mount `./data`, `-p 8088:8088`. Deploy: `rsync` to
`ubuntu@192.168.111.122:/opt/status-page`, `sudo docker compose up -d --build`.

**Phase 5 — MCP.** Hand-author `/openapi.json`, then run `/printing-press` against
`http://192.168.111.122:8088/openapi.json` → `~/printing-press/library/statuspage/` with
`statuspage-pp-cli` + `statuspage-pp-mcp`, matching the other 9 library CLIs. Register in
`~/.claude.json` as `{"type":"stdio","command":".../statuspage-pp-mcp"}` alongside admiral,
hudu, ninjaone, etc. Then `/printing-press-polish`.

**Phase 6 — Prometheus.** Add a `statuspage` scrape job to Steward's Prometheus
(192.168.111.123) targeting `192.168.111.122:8088`, as the 8th exporter. Grafana panels +
alert rules are authored there, not here.

## Verification

- **Adapters:** `go test ./internal/adapters` against saved fixture JSON (captured during
  Phase 1–2 from the live endpoints) — no network in tests.
- **Poller, on-box:** `curl -s localhost:8088/api/v1/health | jq` → expect
  `providers_checked ≈ 150`, `adapter_errors` low single digits, cycle duration < 30 s.
- **Snapshot efficiency:** `curl -sI localhost:8088/api/v1/snapshot` returns an `ETag`;
  re-request with `If-None-Match` → **304**. Confirm gzipped size < 20 KB.
- **Search:** `curl -s 'localhost:8088/api/v1/incidents?q=BGP&limit=5' | jq` returns hits
  with `raw_json` populated.
- **Coverage audit:** `curl -s localhost:8088/api/v1/providers | jq -r '.[]|select(.adapter=="manual")|.name'`
  — the list should match the ~68 known-unresolved, with nothing unexpectedly demoted.
- **Dashboard:** open `http://192.168.111.122:8088/` from the workstation; kill a provider's
  endpoint via a bogus `providers.yaml` entry and confirm it renders red + stale, not blank.
- **MCP:** after Phase 5, in a fresh Claude session ask "what vendor outages are open right
  now" and confirm it calls the `statuspage` MCP and returns the same data as
  `/api/v1/outages`.
- **Metrics:** `curl -s localhost:8088/metrics | grep statuspage_` then confirm the target
  goes green in Steward's Prometheus targets page.

## Risks / open items

- **~68 providers stay unmonitored in v1** (M365 among them — it needs Graph tenant auth).
  They're visible as `manual` on the dashboard and counted in `/metrics`, deliberately.
  Headless-browser scraping is a v2 conversation, not a v1 scope creep.
- **Adapter drift.** Vendors change endpoints without notice. `statuspage_adapter_errors_total`
  is the canary; a provider erroring for > 6 cycles should surface on the dashboard.
- **Politeness.** 218 requests / 10 min ≈ 0.36 req/s aggregate. Statuspage endpoints are
  CDN-cached and public. Add per-cycle jitter and a `User-Agent` identifying us with a
  contact address.
- **Wharf headroom** is fine (8 GB available, 135 GB free) but it already hosts n8n, Honcho,
  open-webui and m365-audit — hence the explicit 512 MB limit.
- **Not in scope:** auth, TLS/nginx, HaloPSA ticket automation. Each is a clean follow-on.
