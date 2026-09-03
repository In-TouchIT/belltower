package adapters

import (
	"context"
	"time"
)

// Indicator represents the status level of a provider
type Indicator string

const (
	IndicatorNone       Indicator = "none"
	IndicatorMinor      Indicator = "minor"
	IndicatorMajor      Indicator = "major"
	IndicatorCritical   Indicator = "critical"
	IndicatorMaintenance  Indicator = "maintenance"
	IndicatorUnknown    Indicator = "unknown"
)

// Component represents a sub-component of a provider's service
type Component struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Incident represents a service incident
type Incident struct {
	ExtID     string    `json:"ext_id"`     // External identifier from the provider
	Title     string    `json:"title"`
	Impact    string    `json:"impact"`     // severity level
	Status    string    `json:"status"`     // operational, degraded, etc.
	StartedAt time.Time `json:"started_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
	URL       string    `json:"url,omitempty"`
	Body      string    `json:"body,omitempty"`
	RawJSON   string    `json:"raw_json,omitempty"` // Complete raw JSON for debugging
}

// Result is the output of an adapter's Fetch method
type Result struct {
	Indicator  Indicator
	Incidents  []Incident
	Components []Component
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

// ProviderFilter filters providers in queries
type ProviderFilter struct {
	Adapter    string
	EnabledOnly bool
	Category   string
}
