package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// WorldSnapshot is the precomputed state document
type WorldSnapshot struct {
	BuiltAt       string            `json:"built_at"`
	Providers     []SnapshotProvider `json:"providers"`
	Incidents     []Incident        `json:"incidents"`
	Components    []Component       `json:"components"`
	Stats         SnapshotStats     `json:"stats"`
}

// SnapshotProvider is a provider in the snapshot context
type SnapshotProvider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Adapter   string `json:"adapter"`
	Indicator string `json:"indicator"` // none|minor|major|critical|maintenance|unknown
	OK        bool   `json:"ok"`
	Tier      int    `json:"tier"`
	PageURL   string `json:"page_url"`
}

// SnapshotStats contains aggregate statistics
type SnapshotStats struct {
	TotalProviders     int `json:"total_providers"`
	Operational        int `json:"operational"`
	Degraded           int `json:"degraded"`
	Outages            int `json:"outages"`
	Maintenance        int `json:"maintenance"`
	Unknown            int `json:"unknown"`
	Unmonitored        int `json:"unmonitored"`
	OpenIncidents      int `json:"open_incidents"`
}

// BuildSnapshot creates a WorldSnapshot from the current database state
func (d *DB) BuildSnapshot() (*WorldSnapshot, error) {
	now := time.Now().UTC()

	// Get all providers
	providers, err := d.GetProviders(ProviderFilter{EnabledOnly: false})
	if err != nil {
		return nil, fmt.Errorf("failed to get providers for snapshot: %w", err)
	}

	// Get latest check for each provider
	snapshotProviders := make([]SnapshotProvider, 0, len(providers))
	stats := SnapshotStats{TotalProviders: len(providers)}

	for _, p := range providers {
		sp := SnapshotProvider{
			ID:        p.ID,
			Name:      p.Name,
			Category:  p.Category,
			Adapter:   p.Adapter,
			Tier:      p.Tier,
			PageURL:   p.PageURL,
		}

		if !p.Enabled || p.Adapter == "manual" {
			sp.Indicator = "unknown"
			sp.OK = false
			stats.Unmonitored++
			snapshotProviders = append(snapshotProviders, sp)
			continue
		}

		// Get latest check
		var check Check
		err := d.QueryRow(`
			SELECT ts, http_code, latency_ms, indicator, ok, err
			FROM checks WHERE endpoint = ?
			ORDER BY ts DESC LIMIT 1
		`, p.Endpoint).Scan(&check.TS, &check.HTTPCode, &check.LatencyMS, &check.Indicator, &check.OK, &check.Err)

		if err == sql.ErrNoRows {
			sp.Indicator = "unknown"
			sp.OK = false
		} else if err != nil {
			sp.Indicator = "unknown"
			sp.OK = false
		} else {
			sp.Indicator = check.Indicator
			sp.OK = check.OK
		}

		// Update stats based on indicator
		switch sp.Indicator {
		case "none":
			stats.Operational++
		case "minor", "major", "critical":
			stats.Outages++
		case "maintenance":
			stats.Maintenance++
		default:
			stats.Unknown++
		}

		snapshotProviders = append(snapshotProviders, sp)
	}

	stats.TotalProviders = len(providers)

	// Get open incidents
	openIncidents, err := d.GetOpenIncidents()
	if err != nil {
		return nil, fmt.Errorf("failed to get open incidents: %w", err)
	}
	stats.OpenIncidents = len(openIncidents)

	// Get all components
	var components []Component
	rows, err := d.Query("SELECT provider_id, name, status, updated_at FROM components ORDER BY provider_id, name")
	if err != nil {
		return nil, fmt.Errorf("failed to query components: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Component
		if err := rows.Scan(&c.ProviderID, &c.Name, &c.Status, &c.UpdatedAt); err != nil {
			continue
		}
		components = append(components, c)
	}

	return &WorldSnapshot{
		BuiltAt:       now.Format(time.RFC3339),
		Providers:     snapshotProviders,
		Incidents:     openIncidents,
		Components:    components,
		Stats:         stats,
	}, nil
}

// ETag computes a hash of the snapshot for cache comparison
func (w *WorldSnapshot) ETag() string {
	h := sha256.New()
	// Use built_at and provider count for a quick hash
	h.Write([]byte(w.BuiltAt))
	h.Write([]byte(fmt.Sprintf("%d", len(w.Providers))))
	h.Write([]byte(fmt.Sprintf("%d", len(w.Incidents))))
	return hex.EncodeToString(h.Sum(nil)[:16]) // First 16 bytes for a compact ETag
}

// MarshalJSON returns the JSON representation
func (w *WorldSnapshot) Marshal() ([]byte, error) {
	return json.MarshalIndent(w, "", "  ")
}
