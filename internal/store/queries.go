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

	query := "SELECT id, name, category, page_url, adapter, endpoint, tier, enabled, notes FROM providers"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY name"

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query providers: %w", err)
	}
	defer rows.Close()

	var providers []Provider
	for rows.Next() {
		var p Provider
		var enabledInt int
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.PageURL, &p.Adapter, &p.Endpoint, &p.Tier, &enabledInt, &p.Notes); err != nil {
			return nil, fmt.Errorf("failed to scan provider: %w", err)
		}
		p.Enabled = enabledInt == 1
		providers = append(providers, p)
	}
	return providers, nil
}

// GetProvider returns a single provider by ID
func (d *DB) GetProvider(id string) (*Provider, error) {
	var p Provider
	var enabledInt int
	err := d.QueryRow(`
		SELECT id, name, category, page_url, adapter, endpoint, tier, enabled, notes
		FROM providers WHERE id = ?
	`, id).Scan(&p.ID, &p.Name, &p.Category, &p.PageURL, &p.Adapter, &p.Endpoint, &p.Tier, &enabledInt, &p.Notes)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabledInt == 1
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
		SELECT i.provider_id, i.ext_id, i.title, i.impact, i.status, i.started_at, i.resolved_at, i.url, i.body, i.raw_json
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

	return scanIncidents(rows)
}

// GetOpenIncidents returns all non-resolved incidents
func (d *DB) GetOpenIncidents() ([]Incident, error) {
	rows, err := d.Query(`
		SELECT provider_id, ext_id, title, impact, status, started_at, resolved_at, url, body, raw_json, first_seen, last_seen
		FROM incidents 
		WHERE resolved_at IS NULL OR resolved_at = '' OR resolved_at = '0001-01-01T00:00:00Z'
		ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query open incidents: %w", err)
	}
	defer rows.Close()

	return scanIncidents(rows)
}

// GetOutages returns providers with non-operational indicators
func (d *DB) GetOutages() ([]Provider, error) {
	// Get providers with recent non-ok checks
	var providers []Provider
	rows, err := d.Query(`
		SELECT DISTINCT p.id, p.name, p.category, p.page_url, p.adapter, p.endpoint, p.tier, p.enabled, p.notes
		FROM providers p
		JOIN (
			SELECT endpoint, indicator FROM checks
			WHERE ts >= datetime('now', '-15 minutes') AND ok = 0
			ORDER BY ts DESC LIMIT 1
		) c ON p.endpoint = c.endpoint
		WHERE p.enabled = 1
		ORDER BY p.tier ASC, p.name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query outages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p Provider
		var enabledInt int
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.PageURL, &p.Adapter, &p.Endpoint, &p.Tier, &enabledInt, &p.Notes); err != nil {
			return nil, fmt.Errorf("failed to scan provider: %w", err)
		}
		p.Enabled = enabledInt == 1
		providers = append(providers, p)
	}
	return providers, nil
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

// Helper function to convert bool to int
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scanIncidents scans rows into incidents
// This function handles the standard incident columns: 12 fields
func scanIncidents(rows *sql.Rows) ([]Incident, error) {
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
	return incidents, nil
}
