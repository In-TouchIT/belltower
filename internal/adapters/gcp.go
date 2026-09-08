package adapters

import (
	"context"
	"net/http"
)

// GCPAdapter handles Google Cloud Platform status.
// Endpoint: https://status.cloud.google.com/incidents.json
type GCPAdapter struct {
	client *http.Client
	ua     string
}

const (
	gcpIncidentsURL = "https://status.cloud.google.com/incidents.json"
	gcpBaseURL      = "https://status.cloud.google.com/"
)

func NewGCPAdapter(client *http.Client, ua string) *GCPAdapter {
	return &GCPAdapter{client: client, ua: ua}
}

func (a *GCPAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = gcpIncidentsURL
	}
	return fetchGoogleStatus(ctx, a.client, a.ua, endpoint, gcpBaseURL)
}

// gcpSeverity is an ordered severity so we can keep the worst live incident.
type gcpSeverity int

const (
	gcpNone gcpSeverity = iota
	gcpMinor
	gcpMajor
	gcpCritical
)

func (s gcpSeverity) indicator() Indicator {
	switch s {
	case gcpCritical:
		return IndicatorCritical
	case gcpMajor:
		return IndicatorMajor
	case gcpMinor:
		return IndicatorMinor
	default:
		return IndicatorNone
	}
}

func gcpIndicator(statusImpact string) gcpSeverity {
	switch statusImpact {
	case "SERVICE_OUTAGE":
		return gcpCritical
	case "SERVICE_DISRUPTION":
		return gcpMajor
	case "SERVICE_INFORMATION":
		return gcpMinor
	default:
		return gcpMinor
	}
}

func gcpImpact(statusImpact, severity string) string {
	switch statusImpact {
	case "SERVICE_OUTAGE":
		return "critical"
	case "SERVICE_DISRUPTION":
		return "major"
	case "SERVICE_INFORMATION":
		return "minor"
	}
	if severity != "" {
		return severity
	}
	return "minor"
}

func gcpComponentStatus(statusImpact string) string {
	switch statusImpact {
	case "SERVICE_OUTAGE":
		return "major_outage"
	case "SERVICE_DISRUPTION":
		return "partial_outage"
	default:
		return "degraded_performance"
	}
}
