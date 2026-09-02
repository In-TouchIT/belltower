-- Status Page Monitor (belltower) database schema
-- Applied automatically on startup

-- Providers table
CREATE TABLE IF NOT EXISTS providers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    category    TEXT NOT NULL,
    page_url    TEXT NOT NULL,
    adapter     TEXT NOT NULL DEFAULT 'manual',
    endpoint    TEXT,
    tier        INTEGER DEFAULT 4,
    enabled     INTEGER DEFAULT 1,
    notes       TEXT
);

-- Checks table (time series of polling results)
CREATE TABLE IF NOT EXISTS checks (
    endpoint    TEXT NOT NULL,
    ts          TEXT NOT NULL,
    http_code   INTEGER,
    latency_ms  INTEGER,
    indicator   TEXT,
    ok          INTEGER,
    err         TEXT,
    PRIMARY KEY (endpoint, ts)
);

-- Incidents table
CREATE TABLE IF NOT EXISTS incidents (
    provider_id TEXT NOT NULL,
    ext_id      TEXT NOT NULL,
    title       TEXT NOT NULL,
    impact      TEXT,
    status      TEXT,
    started_at  TEXT,
    resolved_at TEXT,
    url         TEXT,
    body        TEXT,
    raw_json    TEXT,
    first_seen  TEXT NOT NULL,
    last_seen   TEXT NOT NULL,
    PRIMARY KEY (provider_id, ext_id)
);

-- Components table
CREATE TABLE IF NOT EXISTS components (
    provider_id TEXT NOT NULL,
    name        TEXT NOT NULL,
    status      TEXT,
    updated_at  TEXT,
    PRIMARY KEY (provider_id, name)
);

-- Snapshots table (precomputed full state for fast API responses)
CREATE TABLE IF NOT EXISTS snapshots (
    cycle_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    built_at    TEXT NOT NULL,
    json        BLOB NOT NULL,
    etag        TEXT NOT NULL
);

-- FTS5 index for incidents
CREATE VIRTUAL TABLE IF NOT EXISTS incidents_fts USING fts5(
    title, body, content='incidents', content_rowid='rowid'
);

-- Triggers to keep FTS index synchronized
CREATE TRIGGER IF NOT EXISTS incidents_ai AFTER INSERT ON incidents BEGIN
    INSERT INTO incidents_fts(rowid, title, body)
    VALUES (new.rowid, new.title, new.body);
END;

CREATE TRIGGER IF NOT EXISTS incidents_ad AFTER DELETE ON incidents BEGIN
    INSERT INTO incidents_fts(incidents_fts, rowid, title, body)
    VALUES('delete', old.rowid, old.title, old.body);
END;

CREATE TRIGGER IF NOT EXISTS incidents_au AFTER UPDATE ON incidents BEGIN
    INSERT INTO incidents_fts(incidents_fts, rowid, title, body)
    VALUES('delete', old.rowid, old.title, old.body);
    INSERT INTO incidents_fts(rowid, title, body)
    VALUES (new.rowid, new.title, new.body);
END;

-- Index for faster queries
CREATE INDEX IF NOT EXISTS idx_checks_endpoint_ts ON checks(endpoint, ts DESC);
CREATE INDEX IF NOT EXISTS idx_incidents_provider ON incidents(provider_id);
CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status);
CREATE INDEX IF NOT EXISTS idx_providers_adapter ON providers(adapter);
CREATE INDEX IF NOT EXISTS idx_providers_enabled ON providers(enabled);
