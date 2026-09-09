# Belltower Status Monitoring - Current State

## System Overview
- **Total providers monitored**: 227
- **Automated**: 197 (86.8%)
- **Manual**: 30 (13.2%)
- **Adapters in use**: 20 different adapters

## Available Adapters
1. **statuspage** (144 providers) - Standard StatusPage.io API
2. **rss** (16 providers) - RSS/Atom feeds (dual format support)
3. **statusio** (6 providers) - Status.io platform
4. **cachet** (4 providers) - Cachet open-source platform (3 format variants)
5. **apple** (2 providers) - Apple JSON status feeds
6. **webex** (1 provider) - Cisco Webex custom JSON API
7. **docusign** (1 provider) - DocuSign custom JSON API
8. **generic** (2 providers) - Custom JSON APIs (RingCentral, Rewst)
9. **spa** (14 providers) - Headless Chrome browser rendering
10. **betterstack** (2 providers)
11. **sorryapp** (3 providers)
12. **slack** (1 provider)
13. **salesforce** (1 provider)
14. **heroku** (1 provider)
15. **gworkspace** (1 provider)
16. **gcp** (1 provider)
17. **aws** (1 provider)
18. **statuscast** (1 provider) - For Fastly/SonicWall (StatusCast platform)

## Key Adapter Details

### Cachet Adapter Variants Handled:
- Standard: `{"components": [...], "notices": [...]}`
- Wrapped (ColoBlxs): `{"data": [...]}`
- String state (Servosity): `"state": "operational"`
- Status name field: Uses `status_name` for human-readable component statuses

### StatusPage API Issues Discovered:
- **ADP**: Redirects to contact page (307) - endpoint deprecated
- **Huntress**: Returns 401 (auth required)
- **ThreatDown**: 404 - endpoint changed
- **Trend Micro**: 404 - endpoint changed
- **Zendesk**: 404 - endpoint changed
- **Salesforce**: "Direct API access not allowed"

## Remaining Manual Providers (30)
### No Public Status (4):
- Blackpoint Cyber, Hudu, IRS, Ramp

### Authentication Required (17):
- Okta, Dell, CrowdStrike, ServiceNow, Adobe, Automox, Cisco (legacy), Freshworks, ManageEngine, Microsoft Azure DevOps, Microsoft Power Platform, Netskope, Xfinity, T-Mobile, UptimeRobot, SonicWall, Sophos

### SPA Pages (9):
- Fastly, Stripe, LastPass, Mistral, VIPRE, ZeroTier, Zscaler, Backblaze, ThreatDown

## Deployment
- **Host**: Wharf (192.168.111.122)
- **URL**: http://192.168.111.122:8088
- **Container**: belltower:latest
- **Memory limit**: 512MB
- **Poll interval**: 10 minutes
- **Build**: scratch-based container (~20MB)

## MCP Integration
- **CLI**: /home/ccarson/.local/bin/belltower-pp-cli
- **MCP**: /home/ccarson/.local/bin/belltower-pp-mcp
- **Config**: ~/.config/mcp/mcp.json
- **Tools available**: 19 Belltower-specific MCP tools

## Recent Bug Fixes
1. SQLite concurrency: Batch writes per cycle, 30s busy_timeout
2. Cachet adapter: Correct status code mapping (1=Operational, not performance)
3. Cachet adapter: Support for string states and status_name fields
4. Cachet adapter: Filter "complete" notices as resolved
5. RSS adapter: Added Atom feed support
6. SPA adapter: Headless Chrome with virtual-time-budget for async rendering

## Notes
- Polling 200 endpoints every 10 minutes (~0.33 req/s aggregate)
- Batch writes prevent SQLITE_BUSY errors
- 17 adapters deployed covering diverse status page platforms
- Headless Chrome enables monitoring of SPA-only pages
