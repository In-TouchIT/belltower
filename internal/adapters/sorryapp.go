package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SorryAppAdapter handles SorryApp status pages
// Pattern: <base>/api/v1/status
type SorryAppAdapter struct {
	client *http.Client
	ua     string
}

type SorryAppResponse struct {
	Page struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		State     string `json:"state"` // operational, degraded, under-maintenance, etc.
		StateText string `json:"state_text"`
		URL       string `json:"url"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		Links     struct {
			Components struct {
				Href  string `json:"href"`
				Count int    `json:"count"`
			} `json:"components"`
			Notices struct {
				Href  string `json:"href"`
				Count int    `json:"count"`
			} `json:"notices"`
		} `json:"links"`
	} `json:"page"`
	// Legacy fields (for backwards compatibility)
	Name       string              `json:"name"`
	URL        string              `json:"url"`
	Status     string              `json:"status"`
	Incidents  []SorryAppIncident  `json:"incidents"`
	Components []SorryAppComponent `json:"components"`
}

type SorryAppIncident struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
	URL       string `json:"url"`
	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`
}

type SorryAppComponent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Updated string `json:"updated_at"`
}

func NewSorryAppAdapter(client *http.Client, ua string) *SorryAppAdapter {
	return &SorryAppAdapter{client: client, ua: ua}
}

func (a *SorryAppAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		// Derive from page URL
		if p.PageURL == "" {
			return Result{}, fmt.Errorf("no endpoint for SorryApp provider %s", p.Name)
		}
		base := strings.TrimSuffix(p.PageURL, "/")
		endpoint = base + "/api/v1/status"
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	var resp SorryAppResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse SorryApp response: %w", err)
	}

	// Use the new page.state field if available, fallback to legacy status field
	status := resp.Page.State
	if status == "" {
		status = resp.Status
	}

	result := Result{
		Indicator:  a.mapIndicator(status),
		HTTPStatus: httpResp.StatusCode,
	}

	for _, inc := range resp.Incidents {
		incStatus := NormalizeIncidentStatus(inc.Status)
		// updated_at is just the last edit; treat it as a resolution time
		// only once the incident reports a terminal status.
		var resolvedAt time.Time
		if incStatus == StatusResolved {
			resolvedAt = parseTime(inc.UpdatedAt)
		}
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:       inc.Body,
			Status:     incStatus,
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

func (a *SorryAppAdapter) mapIndicator(status string) Indicator {
	switch status {
	case "operational", "none":
		return IndicatorNone
	case "degraded", "partial", "degraded_performance":
		return IndicatorMinor
	case "partial_outage", "major":
		return IndicatorMajor
	case "major_outage", "down", "critical":
		return IndicatorCritical
	case "maintenance", "under-maintenance":
		return IndicatorMaintenance
	default:
		return IndicatorUnknown
	}
}
