package adapters

import (
	"context"
	"strings"
	"time"
)

// Indicator represents the status level of a provider
type Indicator string

const (
	IndicatorNone        Indicator = "none"
	IndicatorMinor       Indicator = "minor"
	IndicatorMajor       Indicator = "major"
	IndicatorCritical    Indicator = "critical"
	IndicatorMaintenance Indicator = "maintenance"
	IndicatorUnknown     Indicator = "unknown"
)

// Incident lifecycle states. Upstream providers each use their own vocabulary
// ("closed", "Resolved", "postmortem", ...); adapters funnel them through
// NormalizeIncidentStatus so the store and API see a single set of values.
const (
	StatusInvestigating = "investigating"
	StatusIdentified    = "identified"
	StatusMonitoring    = "monitoring"
	StatusResolved      = "resolved"
	StatusMaintenance   = "maintenance"
	StatusScheduled     = "scheduled"
	StatusOpen          = "open"
)

// Component represents a sub-component of a provider's service
type Component struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Incident represents a service incident
type Incident struct {
	ExtID      string    `json:"ext_id"` // External identifier from the provider
	Title      string    `json:"title"`
	Impact     string    `json:"impact"` // severity level
	Status     string    `json:"status"` // one of the Status* constants
	StartedAt  time.Time `json:"started_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
	URL        string    `json:"url,omitempty"`
	Body       string    `json:"body,omitempty"`
	RawJSON    string    `json:"raw_json,omitempty"` // Complete raw JSON for debugging
}

// Resolved reports whether the incident is finished, by either of the two
// signals providers give us: a resolution timestamp or a terminal status.
func (i Incident) Resolved() bool {
	return !i.ResolvedAt.IsZero() || IsResolvedStatus(i.Status)
}

// Result is the output of an adapter's Fetch method
type Result struct {
	Indicator  Indicator
	Incidents  []Incident
	Components []Component

	// HTTPStatus is the status code of the upstream response, or 0 if the
	// request never produced one (DNS failure, timeout, TLS error).
	HTTPStatus int
}

// Adapter is the interface that all status page adapters implement
type Adapter interface {
	Fetch(ctx context.Context, provider ProviderInfo) (Result, error)
}

// ProviderInfo contains the information an adapter needs to fetch status
type ProviderInfo struct {
	ID       string
	Name     string
	Category string
	PageURL  string
	Endpoint string
	Adapter  string
	Tier     int
}

// IsResolvedStatus reports whether a normalized status is terminal.
func IsResolvedStatus(status string) bool {
	switch NormalizeIncidentStatus(status) {
	case StatusResolved:
		return true
	default:
		return false
	}
}

// NormalizeIncidentStatus maps the many provider-specific status vocabularies
// onto the Status* constants. Unrecognized values fall back to StatusOpen so an
// incident is never silently dropped from the "active" view.
func NormalizeIncidentStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "closed", "complete", "completed", "postmortem", "fixed", "done":
		return StatusResolved
	case "investigating":
		return StatusInvestigating
	case "identified":
		return StatusIdentified
	case "monitoring", "verifying":
		return StatusMonitoring
	case "maintenance", "under_maintenance", "under-maintenance", "in_progress", "in progress":
		return StatusMaintenance
	case "scheduled", "upcoming", "planned", "notstartedyet":
		return StatusScheduled
	case "":
		return StatusOpen
	default:
		return StatusOpen
	}
}

// parseTime parses an ISO 8601 / RFC 3339 timestamp, plus the handful of other
// layouts status feeds use in practice.
//
// A value we cannot parse yields the zero time, never time.Now(): callers treat
// the zero time as "unknown", and inventing a timestamp would silently record a
// years-old incident as having started this minute.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02",
	time.RFC1123Z, // RSS pubDate
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
}
