package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ProviderFilter filters providers in queries
type ProviderFilter struct {
	Adapter     string
	Category    string
	EnabledOnly bool
}

// UpsertProvider creates or replaces a provider record
func (d *DB) UpsertProvider(p Provider) error {
	_, err := d.Exec(`
		INSERT OR REPLACE INTO providers (id, name, category, page_url, adapter, endpoint, tier, enabled, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.Name, p.Category, p.PageURL, p.Adapter, p.Endpoint, p.Tier, boolToInt(p.Enabled), p.Notes)
	if err != nil {
		return fmt.Errorf("failed to upsert provider %s: %w", p.ID, err)
	}
	return nil
}

// GetProviders returns all providers, optionally filtered
func (d *DB) GetProviders(filter ProviderFilter) ([]Provider, error) {
	var args []interface{}
	var where []string

	if filter.Adapter != "" {
		where = append(where, "adapter = ?")
		args = append(args, filter.Adapter)
	}
	if filter.EnabledOnly {
		where = append(where, "enabled = 1")
	}
	if filter.Category != "" {
		where = append(where, "category = ?")
		args = append(args, filter.Category)
	}

	query := "SELECT id, name, category, page_url, adapter, COALESCE(endpoint, ''), COALESCE(tier, 4), enabled, COALESCE(notes, '') FROM providers"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY name"

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query providers: %w", err)
	}
	defer rows.Close()

	return scanProviders(rows)
}

// GetProvider returns a single provider by ID
func (d *DB) GetProvider(id string) (*Provider, error) {
	var p Provider
	var enabledInt int
	var notes sql.NullString
	err := d.QueryRow(`
		SELECT id, name, category, page_url, adapter, COALESCE(endpoint, ''), COALESCE(tier, 4), enabled, COALESCE(notes, '')
		FROM providers WHERE id = ?
	`, id).Scan(&p.ID, &p.Name, &p.Category, &p.PageURL, &p.Adapter, &p.Endpoint, &p.Tier, &enabledInt, &notes)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabledInt == 1
	p.Notes = notes.String
	return &p, nil
}

// UpsertCheck inserts a check result
func (d *DB) UpsertCheck(c Check) error {
	_, err := d.Exec(`
		INSERT OR REPLACE INTO checks (endpoint, ts, http_code, latency_ms, indicator, ok, err)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, c.Endpoint, c.TS, c.HTTPCode, c.LatencyMS, c.Indicator, boolToInt(c.OK), c.Err)
	if err != nil {
		return fmt.Errorf("failed to insert check: %w", err)
	}
	return nil
}

// UpsertIncident creates or updates an incident, updating last_seen
func (d *DB) UpsertIncident(i Incident) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if i.LastSeen == "" {
		i.LastSeen = now
	}
	if i.FirstSeen == "" {
		i.FirstSeen = now
	}

	i.Status = strings.ToLower(strings.TrimSpace(i.Status))

	// Store NULL for resolved_at if it's empty or zero time
	var resolvedAt interface{}
	if i.ResolvedAt == "" || i.ResolvedAt == "0001-01-01T00:00:00Z" {
		resolvedAt = nil
	} else {
		resolvedAt = i.ResolvedAt
	}

	_, err := d.Exec(`
		INSERT INTO incidents (provider_id, ext_id, title, impact, status, started_at, resolved_at, url, body, raw_json, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_id, ext_id) DO UPDATE SET
			title = excluded.title,
			impact = excluded.impact,
			status = excluded.status,
			started_at = excluded.started_at,
			resolved_at = CASE 
				WHEN excluded.resolved_at IS NOT NULL AND excluded.resolved_at != '0001-01-01T00:00:00Z' 
				THEN excluded.resolved_at 
				ELSE incidents.resolved_at 
			END,
			url = excluded.url,
			body = excluded.body,
			raw_json = excluded.raw_json,
			last_seen = excluded.last_seen
	`, i.ProviderID, i.ExtID, i.Title, i.Impact, i.Status, i.StartedAt, resolvedAt, i.URL, i.Body, i.RawJSON, i.FirstSeen, i.LastSeen)
	if err != nil {
		return fmt.Errorf("failed to upsert incident: %w", err)
	}
	return nil
}

// UpsertComponent creates or updates a component
func (d *DB) UpsertComponent(c Component) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if c.UpdatedAt == "" {
		c.UpdatedAt = now
	}

	_, err := d.Exec(`
		INSERT OR REPLACE INTO components (provider_id, name, status, updated_at)
		VALUES (?, ?, ?, ?)
	`, c.ProviderID, c.Name, c.Status, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert component: %w", err)
	}
	return nil
}

// SaveSnapshot stores a precomputed snapshot
func (d *DB) SaveSnapshot(builtAt string, jsonData []byte, etag string) (int64, error) {
	result, err := d.Exec(`
		INSERT INTO snapshots (built_at, json, etag) VALUES (?, ?, ?)
	`, builtAt, jsonData, etag)
	if err != nil {
		return 0, fmt.Errorf("failed to save snapshot: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert id: %w", err)
	}
	return id, nil
}

// GetLatestSnapshot returns the most recent snapshot
func (d *DB) GetLatestSnapshot() (*Snapshot, error) {
	var s Snapshot
	var cycleID int64
	err := d.QueryRow(`
		SELECT cycle_id, built_at, json, etag FROM snapshots ORDER BY cycle_id DESC LIMIT 1
	`).Scan(&cycleID, &s.BuiltAt, &s.JSON, &s.ETag)
	if err != nil {
		return nil, err
	}
	s.CycleID = cycleID
	return &s, nil
}

// SearchIncidents searches incidents using FTS5
func (d *DB) SearchIncidents(query string, limit int) ([]Incident, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := d.Query(`
		SELECT i.provider_id, i.ext_id, i.title, i.impact, i.status, i.started_at, i.resolved_at,
		       i.url, i.body, i.raw_json, i.first_seen, i.last_seen
		FROM incidents i
		JOIN incidents_fts f ON i.rowid = f.rowid
		WHERE incidents_fts MATCH ?
		ORDER BY i.started_at DESC
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search incidents: %w", err)
	}
	defer rows.Close()

	return ScanIncidents(rows)
}

// GetOpenIncidents returns all incidents that are still active.
//
// Two independent signals close an incident, and both must be checked: some
// providers stamp a resolution timestamp, others only flip the status text.
// Keying off resolved_at alone left every "resolved" incident from those
// providers permanently in the active list.
func (d *DB) GetOpenIncidents() ([]Incident, error) {
	rows, err := d.Query(`
		SELECT provider_id, ext_id, title, impact, status, started_at, resolved_at, url, body, raw_json, first_seen, last_seen
		FROM incidents
		WHERE (resolved_at IS NULL OR resolved_at = '' OR resolved_at = '0001-01-01T00:00:00Z')
		  AND LOWER(COALESCE(status, '')) NOT IN ('resolved', 'closed', 'completed', 'postmortem')
		ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query open incidents: %w", err)
	}
	defer rows.Close()

	return ScanIncidents(rows)
}

// GetOutages returns providers whose most recent check is non-operational.
//
// "Most recent" has to be computed per endpoint: a single LIMIT 1 over the
// whole checks table (as this once did) returns one row for the entire fleet,
// so at most one provider could ever be reported as down.
func (d *DB) GetOutages(within time.Duration) ([]Provider, error) {
	// checks.ts is stored as RFC3339, so the cutoff is formatted the same way.
	// Comparing against SQLite's datetime() output would compare 'T' to ' '
	// and silently match anything from the same calendar day onward.
	cutoff := time.Now().UTC().Add(-within).Format(time.RFC3339)

	rows, err := d.Query(`
		SELECT p.id, p.name, p.category, p.page_url, p.adapter, COALESCE(p.endpoint, ''), COALESCE(p.tier, 4), p.enabled, COALESCE(p.notes, '')
		FROM providers p
		JOIN (
			SELECT c.endpoint, c.indicator, c.ok
			FROM checks c
			JOIN (
				SELECT endpoint, MAX(ts) AS ts FROM checks WHERE ts >= ? GROUP BY endpoint
			) latest ON latest.endpoint = c.endpoint AND latest.ts = c.ts
		) c ON p.endpoint = c.endpoint
		WHERE p.enabled = 1
		  AND (c.ok = 0 OR c.indicator NOT IN ('none', 'operational'))
		ORDER BY p.tier ASC, p.name ASC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query outages: %w", err)
	}
	defer rows.Close()

	return scanProviders(rows)
}

// LatestCheck is the most recent check recorded for an endpoint.
type LatestCheck struct {
	TS        string
	Indicator string
	LatencyMS int
	HTTPCode  int
	OK        bool
	Err       string
}

// GetLatestChecks returns the most recent check for every endpoint, keyed by
// endpoint. Callers that need status for many providers use this instead of
// issuing one "ORDER BY ts DESC LIMIT 1" query per provider.
func (d *DB) GetLatestChecks() (map[string]LatestCheck, error) {
	rows, err := d.Query(`
		SELECT c.endpoint, c.ts, COALESCE(c.indicator, ''), COALESCE(c.latency_ms, 0),
		       COALESCE(c.http_code, 0), COALESCE(c.ok, 0), COALESCE(c.err, '')
		FROM checks c
		JOIN (
			SELECT endpoint, MAX(ts) AS ts FROM checks GROUP BY endpoint
		) latest ON latest.endpoint = c.endpoint AND latest.ts = c.ts
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query latest checks: %w", err)
	}
	defer rows.Close()

	out := make(map[string]LatestCheck)
	for rows.Next() {
		var endpoint string
		var c LatestCheck
		var okInt int
		if err := rows.Scan(&endpoint, &c.TS, &c.Indicator, &c.LatencyMS, &c.HTTPCode, &okInt, &c.Err); err != nil {
			return nil, fmt.Errorf("failed to scan check: %w", err)
		}
		c.OK = okInt == 1
		out[endpoint] = c
	}
	return out, rows.Err()
}

// PruneOldChecks removes check records older than the specified duration
func (d *DB) PruneOldChecks(maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	result, err := d.Exec("DELETE FROM checks WHERE ts < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to prune old checks: %w", err)
	}
	return result.RowsAffected()
}

// PruneOldSnapshots removes snapshots older than the specified number
func (d *DB) PruneOldSnapshots(keep int) (int64, error) {
	result, err := d.Exec(`
		DELETE FROM snapshots WHERE cycle_id NOT IN (
			SELECT cycle_id FROM snapshots ORDER BY cycle_id DESC LIMIT ?
		)
	`, keep)
	if err != nil {
		return 0, fmt.Errorf("failed to prune old snapshots: %w", err)
	}
	return result.RowsAffected()
}

// scanProviders scans provider rows into a slice.
func scanProviders(rows *sql.Rows) ([]Provider, error) {
	var providers []Provider
	for rows.Next() {
		var p Provider
		var enabledInt int
		var notes sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.PageURL, &p.Adapter, &p.Endpoint, &p.Tier, &enabledInt, &notes); err != nil {
			return nil, fmt.Errorf("failed to scan provider: %w", err)
		}
		p.Enabled = enabledInt == 1
		p.Notes = notes.String
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// PruneResolvedIncidents deletes resolved incidents last seen before the
// cutoff. Without this, every incident an upstream summary has ever listed
// accumulates forever - the feeds return recent history on every poll, not
// just what is currently active.
func (d *DB) PruneResolvedIncidents(maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	result, err := d.Exec(`
		DELETE FROM incidents
		WHERE last_seen < ?
		  AND (
		    (resolved_at IS NOT NULL AND resolved_at != '' AND resolved_at != '0001-01-01T00:00:00Z')
		    OR LOWER(COALESCE(status, '')) IN ('resolved', 'closed', 'completed', 'postmortem')
		  )
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to prune resolved incidents: %w", err)
	}
	return result.RowsAffected()
}

// Helper function to convert bool to int
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ScanIncidents scans rows into incidents.
// Callers must select the 12 incident columns in schema order.
func ScanIncidents(rows *sql.Rows) ([]Incident, error) {
	var incidents []Incident
	for rows.Next() {
		var i Incident
		var rawJSON sql.NullString
		var resolvedAt sql.NullString

		err := rows.Scan(
			&i.ProviderID, &i.ExtID, &i.Title, &i.Impact, &i.Status,
			&i.StartedAt, &resolvedAt, &i.URL, &i.Body,
			&rawJSON, &i.FirstSeen, &i.LastSeen,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan incident: %w", err)
		}
		if resolvedAt.Valid {
			i.ResolvedAt = resolvedAt.String
		} else {
			i.ResolvedAt = ""
		}
		if rawJSON.Valid {
			i.RawJSON = rawJSON.String
		}
		incidents = append(incidents, i)
	}
	return incidents, rows.Err()
}
