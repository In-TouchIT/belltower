package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Status  string `json:"status"`
	Incidents []InstatusIncident `json:"incidents"`
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
	ID    string `json:"id"`
	Name  string `json:"name"`
	Status string `json:"status"`
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

	data, err := a.fetch(ctx, endpoint)
	if err != nil {
		return Result{}, err
	}

	var resp InstatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse Instatus response: %w", err)
	}

	result := Result{
		Indicator: a.mapIndicator(resp.Status),
	}

	for _, inc := range resp.Incidents {
		result.Incidents = append(result.Incidents, Incident{
			ExtID:     inc.ID,
			Title:     inc.Name,
			Body:      inc.Body,
			Status:    inc.Status,
			Impact:    inc.Impact,
			StartedAt: parseTime(inc.StartedAt),
			ResolvedAt: parseTime(inc.EndedAt),
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

func (a *InstatusAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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
