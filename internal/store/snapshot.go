package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// WorldSnapshot is the precomputed state document
type WorldSnapshot struct {
	BuiltAt    string             `json:"built_at"`
	Providers  []SnapshotProvider `json:"providers"`
	Incidents  []Incident         `json:"incidents"`
	Components []Component        `json:"components"`
	Stats      SnapshotStats      `json:"stats"`
}

// SnapshotProvider is a provider in the snapshot context
type SnapshotProvider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Adapter   string `json:"adapter"`
	Indicator string `json:"indicator"` // none|minor|major|critical|maintenance|unknown
	// OK reports whether the last poll reached the provider at all. It says
	// nothing about the provider's health - read Indicator for that.
	OK        bool   `json:"ok"`
	Tier      int    `json:"tier"`
	PageURL   string `json:"page_url"`
	LastCheck string `json:"last_check,omitempty"`
	LatencyMS int    `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Indicator values, mirroring adapters.Indicator. Duplicated rather than
// imported to keep store free of a dependency on adapters.
type Indicator = string

const (
	IndicatorNone        Indicator = "none"
	IndicatorMinor       Indicator = "minor"
	IndicatorMajor       Indicator = "major"
	IndicatorCritical    Indicator = "critical"
	IndicatorMaintenance Indicator = "maintenance"
	IndicatorUnknown     Indicator = "unknown"
)

// SnapshotStats contains aggregate statistics
type SnapshotStats struct {
	TotalProviders int `json:"total_providers"`
	Operational    int `json:"operational"`
	Degraded       int `json:"degraded"`
	Outages        int `json:"outages"`
	Maintenance    int `json:"maintenance"`
	Unknown        int `json:"unknown"`
	Unmonitored    int `json:"unmonitored"`
	OpenIncidents  int `json:"open_incidents"`
}

// BuildSnapshot creates a WorldSnapshot from the current database state.
func (d *DB) BuildSnapshot() (*WorldSnapshot, error) {
	now := time.Now().UTC()

	providers, err := d.GetProviders(ProviderFilter{EnabledOnly: false})
	if err != nil {
		return nil, fmt.Errorf("failed to get providers for snapshot: %w", err)
	}

	// One query for every endpoint's latest check, rather than one query per
	// provider (which was ~220 round trips on every snapshot build).
	latest, err := d.GetLatestChecks()
	if err != nil {
		return nil, fmt.Errorf("failed to get latest checks: %w", err)
	}

	snapshotProviders := make([]SnapshotProvider, 0, len(providers))
	stats := SnapshotStats{TotalProviders: len(providers)}

	for _, p := range providers {
		sp := SnapshotProvider{
			ID:       p.ID,
			Name:     p.Name,
			Category: p.Category,
			Adapter:  p.Adapter,
			Tier:     p.Tier,
			PageURL:  p.PageURL,
		}

		if !p.Enabled || p.Adapter == "manual" || p.Endpoint == "" {
			sp.Indicator = string(IndicatorUnknown)
			sp.OK = false
			stats.Unmonitored++
			snapshotProviders = append(snapshotProviders, sp)
			continue
		}

		check, ok := latest[p.Endpoint]
		if !ok || check.Indicator == "" {
			sp.Indicator = string(IndicatorUnknown)
			sp.OK = false
		} else {
			sp.Indicator = check.Indicator
			// OK means "we reached the provider", not "the provider is
			// healthy" - consumers must read Indicator for health.
			sp.OK = check.OK
			sp.LastCheck = check.TS
			sp.LatencyMS = check.LatencyMS
			sp.Error = check.Err
		}

		switch sp.Indicator {
		case string(IndicatorNone):
			stats.Operational++
		case string(IndicatorMinor):
			stats.Degraded++
		case string(IndicatorMajor), string(IndicatorCritical):
			stats.Outages++
		case string(IndicatorMaintenance):
			stats.Maintenance++
		default:
			stats.Unknown++
		}

		snapshotProviders = append(snapshotProviders, sp)
	}

	openIncidents, err := d.GetOpenIncidents()
	if err != nil {
		return nil, fmt.Errorf("failed to get open incidents: %w", err)
	}
	stats.OpenIncidents = len(openIncidents)

	components, err := d.getComponents()
	if err != nil {
		return nil, err
	}

	return &WorldSnapshot{
		BuiltAt:    now.Format(time.RFC3339),
		Providers:  snapshotProviders,
		Incidents:  openIncidents,
		Components: components,
		Stats:      stats,
	}, nil
}

func (d *DB) getComponents() ([]Component, error) {
	rows, err := d.Query(`
		SELECT provider_id, name, COALESCE(status, ''), COALESCE(updated_at, '')
		FROM components ORDER BY provider_id, name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query components: %w", err)
	}
	defer rows.Close()

	var components []Component
	for rows.Next() {
		var c Component
		if err := rows.Scan(&c.ProviderID, &c.Name, &c.Status, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan component: %w", err)
		}
		components = append(components, c)
	}
	return components, rows.Err()
}

// ETag computes a hash of the snapshot's content for cache comparison.
//
// BuiltAt is deliberately excluded: it changes every polling cycle, so hashing
// it produced a fresh ETag every 10 minutes even when nothing had changed,
// which is exactly the case conditional requests exist to avoid.
func (w *WorldSnapshot) ETag() string {
	h := sha256.New()
	for _, p := range w.Providers {
		fmt.Fprintf(h, "%s\x1f%s\x1f%t\x1e", p.ID, p.Indicator, p.OK)
	}
	for _, i := range w.Incidents {
		fmt.Fprintf(h, "%s\x1f%s\x1f%s\x1f%s\x1e", i.ProviderID, i.ExtID, i.Status, i.ResolvedAt)
	}
	for _, c := range w.Components {
		fmt.Fprintf(h, "%s\x1f%s\x1f%s\x1e", c.ProviderID, c.Name, c.Status)
	}
	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// MarshalJSON returns the JSON representation
func (w *WorldSnapshot) Marshal() ([]byte, error) {
	return json.MarshalIndent(w, "", "  ")
}
