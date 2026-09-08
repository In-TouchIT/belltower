package adapters

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"encoding/json"
)

// SalesforceAdapter handles Salesforce Trust status.
// Endpoint: https://api.status.salesforce.com/v1/incidents
type SalesforceAdapter struct {
	client *http.Client
	ua     string
}

const (
	salesforceIncidentsURL = "https://api.status.salesforce.com/v1/incidents"
	salesforceIncidentPage = "https://status.salesforce.com/incidents/"
)

// SalesforceIncident mirrors the Trust API's real schema. Note there is no
// name/details/url field: a previous version of this adapter looked for those,
// which is why every Salesforce incident was stored with an empty title.
type SalesforceIncident struct {
	ID                    string   `json:"id"`
	ExternalID            string   `json:"externalId"`
	Status                string   `json:"status"` // "Active" | "Resolved"
	Type                  string   `json:"type"`   // "Degradation" | "Disruption"
	CreatedAt             string   `json:"createdAt"`
	UpdatedAt             string   `json:"updatedAt"`
	ServiceKeys           []string `json:"serviceKeys"`
	InstanceKeys          []string `json:"instanceKeys"`
	AffectsAll            bool     `json:"affectsAll"`
	IsCore                bool     `json:"isCore"`
	AdditionalInformation string   `json:"additionalInformation"`

	Message struct {
		RootCause        string `json:"rootCause"`
		ActionPlan       string `json:"actionPlan"`
		PathToResolution string `json:"pathToResolution"`
	} `json:"message"`

	IncidentImpacts []struct {
		ID        int    `json:"id"`
		Type      string `json:"type"`
		Severity  string `json:"severity"` // "minor" | "major"
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"IncidentImpacts"`

	IncidentEvents []struct {
		Type      string `json:"type"`
		Message   string `json:"message"`
		CreatedAt string `json:"createdAt"`
	} `json:"IncidentEvents"`
}

func NewSalesforceAdapter(client *http.Client, ua string) *SalesforceAdapter {
	return &SalesforceAdapter{client: client, ua: ua}
}

func (a *SalesforceAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = salesforceIncidentsURL
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}

	var incidents []SalesforceIncident
	if err := json.Unmarshal(httpResp.Body, &incidents); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse Salesforce response: %w", err)
	}

	result := Result{Indicator: IndicatorNone, HTTPStatus: httpResp.StatusCode}

	for _, inc := range incidents {
		status := NormalizeIncidentStatus(inc.Status)
		resolved := status == StatusResolved

		severity, startedAt, endedAt := salesforceImpact(inc)
		if !resolved {
			if escalated := a.mapSeverity(severity); indicatorRank(escalated) > indicatorRank(result.Indicator) {
				result.Indicator = escalated
			}
		}

		if startedAt.IsZero() {
			startedAt = parseTime(inc.CreatedAt)
		}
		var resolvedAt time.Time
		if resolved {
			resolvedAt = endedAt
			if resolvedAt.IsZero() {
				resolvedAt = parseTime(inc.UpdatedAt)
			}
		}

		extID := inc.ExternalID
		if extID == "" {
			extID = inc.ID
		}

		result.Incidents = append(result.Incidents, Incident{
			ExtID:      extID,
			Title:      salesforceTitle(inc),
			Body:       salesforceBody(inc),
			Status:     status,
			Impact:     severity,
			StartedAt:  startedAt,
			ResolvedAt: resolvedAt,
			URL:        salesforceIncidentPage + extID,
		})
	}

	return result, nil
}

// salesforceTitle synthesizes a title. The API has no title field, so it is
// built from the impact type and the affected services, e.g.
// "Feature Perf Degradation - coreService".
func salesforceTitle(inc SalesforceIncident) string {
	label := ""
	if len(inc.IncidentImpacts) > 0 {
		label = humanizeCamel(inc.IncidentImpacts[0].Type)
	}
	if label == "" {
		label = inc.Type
	}
	if label == "" {
		label = "Incident"
	}

	services := inc.ServiceKeys
	if len(services) == 0 {
		services = inc.InstanceKeys
	}
	if len(services) > 0 {
		limit := len(services)
		if limit > 3 {
			limit = 3
		}
		suffix := strings.Join(services[:limit], ", ")
		if len(services) > limit {
			suffix += fmt.Sprintf(" and %d more", len(services)-limit)
		}
		return label + " - " + suffix
	}
	return label
}

// salesforceBody prefers the most recent event message, falling back to the
// structured post-incident fields.
func salesforceBody(inc SalesforceIncident) string {
	if n := len(inc.IncidentEvents); n > 0 {
		for i := n - 1; i >= 0; i-- {
			if msg := strings.TrimSpace(inc.IncidentEvents[i].Message); msg != "" {
				return msg
			}
		}
	}

	var parts []string
	if inc.Message.RootCause != "" {
		parts = append(parts, "Root cause: "+inc.Message.RootCause)
	}
	if inc.Message.ActionPlan != "" {
		parts = append(parts, "Action plan: "+inc.Message.ActionPlan)
	}
	if inc.AdditionalInformation != "" {
		parts = append(parts, inc.AdditionalInformation)
	}
	return strings.Join(parts, "\n\n")
}

// salesforceImpact returns the worst severity across the incident's impacts,
// plus the earliest start and latest end across them.
func salesforceImpact(inc SalesforceIncident) (severity string, startedAt, endedAt time.Time) {
	for _, impact := range inc.IncidentImpacts {
		if impact.Severity == "major" || severity == "" {
			if severity != "major" {
				severity = impact.Severity
			}
		}
		if start := parseTime(impact.StartTime); !start.IsZero() {
			if startedAt.IsZero() || start.Before(startedAt) {
				startedAt = start
			}
		}
		if end := parseTime(impact.EndTime); !end.IsZero() {
			if end.After(endedAt) {
				endedAt = end
			}
		}
	}
	if severity == "" {
		// Fall back to the incident-level classification.
		if strings.EqualFold(inc.Type, "Disruption") {
			severity = "major"
		} else {
			severity = "minor"
		}
	}
	return severity, startedAt, endedAt
}

// humanizeCamel turns "featurePerfDegradation" into "Feature Perf Degradation".
func humanizeCamel(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	out := b.String()
	return strings.ToUpper(out[:1]) + out[1:]
}

func (a *SalesforceAdapter) mapSeverity(severity string) Indicator {
	switch strings.ToLower(severity) {
	case "major", "critical":
		return IndicatorMajor
	case "minor", "degradation":
		return IndicatorMinor
	default:
		return IndicatorMinor
	}
}

// indicatorRank orders indicators so the worst live incident wins.
func indicatorRank(i Indicator) int {
	switch i {
	case IndicatorCritical:
		return 4
	case IndicatorMajor:
		return 3
	case IndicatorMinor:
		return 2
	case IndicatorMaintenance:
		return 1
	default:
		return 0
	}
}
