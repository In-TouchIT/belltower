package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// InstatusAdapter handles Instatus status pages
// Pattern: <base>/summary.json
type InstatusAdapter struct {
	client *http.Client
	ua     string
}

type InstatusResponse struct {
	Page struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"page"`
	Status     string              `json:"status"`
	Incidents  []InstatusIncident  `json:"incidents"`
	Components []InstatusComponent `json:"components"`
}

type InstatusIncident struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
	URL       string `json:"url"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

type InstatusComponent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Updated string `json:"updated_at"`
}

func NewInstatusAdapter(client *http.Client, ua string) *InstatusAdapter {
	return &InstatusAdapter{client: client, ua: ua}
}

func (a *InstatusAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		// Derive from page URL
		if p.PageURL == "" {
			return Result{}, fmt.Errorf("no endpoint for Instatus provider %s", p.Name)
		}
		base := strings.TrimSuffix(p.PageURL, "/")
		endpoint = base + "/summary.json"
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	var resp InstatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse Instatus response: %w", err)
	}

	result := Result{
		Indicator:  a.mapIndicator(resp.Status),
		HTTPStatus: httpResp.StatusCode,
	}

	for _, inc := range resp.Incidents {
		status := NormalizeIncidentStatus(inc.Status)
		resolvedAt := parseTime(inc.EndedAt)
		if !resolvedAt.IsZero() {
			status = StatusResolved
		}
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Name,
			Body:       inc.Body,
			Status:     status,
			Impact:     inc.Impact,
			StartedAt:  parseTime(inc.StartedAt),
			ResolvedAt: resolvedAt,
			URL:        inc.URL,
			RawJSON:    string(data),
		})
	}

	for _, comp := range resp.Components {
		result.Components = append(result.Components, Component{
			Name:      comp.Name,
			Status:    comp.Status,
			UpdatedAt: parseTime(comp.Updated),
		})
	}

	return result, nil
}

func (a *InstatusAdapter) mapIndicator(status string) Indicator {
	switch status {
	case "operational", "none":
		return IndicatorNone
	case "degraded", "partial":
		return IndicatorMinor
	case "partial_outage", "major":
		return IndicatorMajor
	case "major_outage", "down":
		return IndicatorCritical
	case "maintenance":
		return IndicatorMaintenance
	default:
		return IndicatorUnknown
	}
}
