package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SorryAppAdapter handles SorryApp status pages
// Pattern: <base>/api/v1/status
type SorryAppAdapter struct {
	client *http.Client
	ua     string
}

type SorryAppResponse struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Incidents []SorryAppIncident `json:"incidents"`
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
	ID    string `json:"id"`
	Name  string `json:"name"`
	Status string `json:"status"`
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

	data, err := a.fetch(ctx, endpoint)
	if err != nil {
		return Result{}, err
	}

	var resp SorryAppResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse SorryApp response: %w", err)
	}

	result := Result{
		Indicator: a.mapIndicator(resp.Status),
	}

	for _, inc := range resp.Incidents {
		result.Incidents = append(result.Incidents, Incident{
			ExtID:     inc.ID,
			Title:     inc.Title,
			Body:      inc.Body,
			Status:    inc.Status,
			Impact:    inc.Impact,
			StartedAt: parseTime(inc.StartedAt),
			ResolvedAt: parseTime(inc.UpdatedAt),
			URL:       inc.URL,
			RawJSON:   string(data),
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

func (a *SorryAppAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (a *SorryAppAdapter) mapIndicator(status string) Indicator {
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
