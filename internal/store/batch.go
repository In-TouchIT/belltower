package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PollResult holds the result of polling one endpoint, ready for batch write.
type PollResult struct {
	Check     Check
	Incidents []Incident
	Components []Component
	Providers []Provider // providers sharing this endpoint
}

// SavePollResults writes all poll results from a single cycle in one
// transaction. This avoids SQLITE_BUSY errors from concurrent writers
// and makes each cycle atomic: either all results from the cycle persist,
// or none do.
func (d *DB) SavePollResults(results []PollResult) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	for _, r := range results {
		_, err := tx.Exec(`
			INSERT OR REPLACE INTO checks (endpoint, ts, http_code, latency_ms, indicator, ok, err)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, r.Check.Endpoint, r.Check.TS, r.Check.HTTPCode, r.Check.LatencyMS,
			r.Check.Indicator, boolToInt(r.Check.OK), r.Check.Err)
		if err != nil {
			return fmt.Errorf("failed to insert check for %s: %w", r.Check.Endpoint, err)
		}

		for _, inc := range r.Incidents {
			if inc.ExtID == "" {
				continue
			}
			if dbErr := upsertIncidentTx(tx, inc); dbErr != nil {
				return fmt.Errorf("failed to upsert incident: %w", dbErr)
			}
		}

		for _, comp := range r.Components {
			if comp.Name == "" {
				continue
			}
			if dbErr := upsertComponentTx(tx, comp); dbErr != nil {
				return fmt.Errorf("failed to upsert component: %w", dbErr)
			}
		}
	}

	return tx.Commit()
}

func upsertIncidentTx(tx *sql.Tx, i Incident) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if i.LastSeen == "" {
		i.LastSeen = now
	}
	if i.FirstSeen == "" {
		i.FirstSeen = now
	}

	i.Status = strings.ToLower(strings.TrimSpace(i.Status))

	var resolvedAt interface{}
	if i.ResolvedAt == "" || i.ResolvedAt == "0001-01-01T00:00:00Z" {
		resolvedAt = nil
	} else {
		resolvedAt = i.ResolvedAt
	}

	_, err := tx.Exec(`
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
	`, i.ProviderID, i.ExtID, i.Title, i.Impact, i.Status, i.StartedAt,
		resolvedAt, i.URL, i.Body, i.RawJSON, i.FirstSeen, i.LastSeen)
	return err
}

func upsertComponentTx(tx *sql.Tx, c Component) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if c.UpdatedAt == "" {
		c.UpdatedAt = now
	}

	_, err := tx.Exec(`
		INSERT OR REPLACE INTO components (provider_id, name, status, updated_at)
		VALUES (?, ?, ?, ?)
	`, c.ProviderID, c.Name, c.Status, c.UpdatedAt)
	return err
}
