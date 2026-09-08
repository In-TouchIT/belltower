package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// BetterStackAdapter handles BetterStack status pages
// Pattern: <base>/index.json
type BetterStackAdapter struct {
	client *http.Client
	ua     string
}

type BetterStackResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			CompanyName    string                 `json:"company_name"`
			CompanyURL     string                 `json:"company_url"`
			Status         string                 `json:"status"`          // Legacy field
			AggregateState string                 `json:"aggregate_state"` // New field
			Incidents      []BetterStackIncident  `json:"incidents"`
			Components     []BetterStackComponent `json:"components"`
		} `json:"attributes"`
	} `json:"data"`
	Included []BetterStackIncluded `json:"included"`
	// Legacy fields (for backwards compatibility)
	Name       string                 `json:"name"`
	URL        string                 `json:"url"`
	Status     string                 `json:"status"`
	Incidents  []BetterStackIncident  `json:"incidents"`
	Components []BetterStackComponent `json:"components"`
}

type BetterStackIncluded struct {
	ID         string                        `json:"id"`
	Type       string                        `json:"type"`
	Attributes BetterStackIncludedAttributes `json:"attributes"`
}

type BetterStackIncludedAttributes struct {
	Name           string `json:"public_name"`
	Status         string `json:"status"`
	Severity       string `json:"severity"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at"`
	Title          string `json:"name"`
	Description    string `json:"description"`
	Body           string `json:"body"`
	AggregateState string `json:"aggregate_state"`
}

type BetterStackIncident struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Status  string `json:"status"`
	Impact  string `json:"impact"`
	URL     string `json:"url"`
	StartAt string `json:"started_at"`
	EndAt   string `json:"ended_at"`
}

type BetterStackComponent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Updated string `json:"updated_at"`
}

func NewBetterStackAdapter(client *http.Client, ua string) *BetterStackAdapter {
	return &BetterStackAdapter{client: client, ua: ua}
}

func (a *BetterStackAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for BetterStack provider %s", p.Name)
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	var resp BetterStackResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse BetterStack response: %w", err)
	}

	// Determine status from either new or legacy format
	status := resp.Data.Attributes.AggregateState
	if status == "" {
		status = resp.Data.Attributes.Status
	}
	if status == "" {
		status = resp.Status // Legacy fallback
	}

	result := Result{
		Indicator:  a.mapIndicator(status),
		HTTPStatus: httpResp.StatusCode,
	}

	// Parse components and incidents from included array (new JSON:API format)
	for _, item := range resp.Included {
		if item.Type == "status_page_resource" {
			// This is a component
			result.Components = append(result.Components, Component{
				Name:      item.Attributes.Name,
				Status:    item.Attributes.Status,
				UpdatedAt: parseTime(item.Attributes.StartedAt), // Use started_at as update time if available
			})
		} else if item.Type == "status_report" {
			// This is an incident/maintenance
			endedAt := parseTime(item.Attributes.EndedAt)
			reportStatus := NormalizeIncidentStatus(item.Attributes.Status)
			if !endedAt.IsZero() {
				reportStatus = StatusResolved
			}
			incident := Incident{
				ExtID:      item.ID,
				Title:      item.Attributes.Title,
				Body:       item.Attributes.Description,
				Status:     reportStatus,
				StartedAt:  parseTime(item.Attributes.StartedAt),
				ResolvedAt: endedAt,
				RawJSON:    string(data),
			}
			if item.Attributes.Title == "" {
				incident.Title = item.Attributes.Name
			}
			if item.Attributes.Description == "" {
				incident.Body = item.Attributes.Body
			}
			result.Incidents = append(result.Incidents, incident)
		}
	}

	// Fallback to legacy incident parsing
	if len(result.Incidents) == 0 {
		for _, inc := range resp.Incidents {
			endedAt := parseTime(inc.EndAt)
			legacyStatus := NormalizeIncidentStatus(inc.Status)
			if !endedAt.IsZero() {
				legacyStatus = StatusResolved
			}
			result.Incidents = append(result.Incidents, Incident{
				ExtID:      inc.ID,
				Title:      inc.Name,
				Body:       inc.Body,
				Status:     legacyStatus,
				Impact:     inc.Impact,
				StartedAt:  parseTime(inc.StartAt),
				ResolvedAt: endedAt,
				URL:        inc.URL,
				RawJSON:    string(data),
			})
		}
	}

	// Fallback to legacy component parsing
	if len(result.Components) == 0 {
		for _, comp := range resp.Components {
			result.Components = append(result.Components, Component{
				Name:      comp.Name,
				Status:    comp.Status,
				UpdatedAt: parseTime(comp.Updated),
			})
		}
	}

	return result, nil
}

func (a *BetterStackAdapter) mapIndicator(status string) Indicator {
	switch status {
	case "operational", "operational-major", "none":
		return IndicatorNone
	case "partially_degraded", "degraded":
		return IndicatorMinor
	case "partial_outage", "major":
		return IndicatorMajor
	case "major_outage", "critical", "down":
		return IndicatorCritical
	case "maintenance":
		return IndicatorMaintenance
	default:
		return IndicatorUnknown
	}
}
