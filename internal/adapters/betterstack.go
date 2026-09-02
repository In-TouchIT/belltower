package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// BetterStackAdapter handles BetterStack status pages
// Pattern: <base>/index.json
type BetterStackAdapter struct {
	client *http.Client
	ua     string
}

type BetterStackResponse struct {
	Name      string                    `json:"name"`
	URL       string                    `json:"url"`
	Status    string                    `json:"status"`
	Incidents []BetterStackIncident     `json:"incidents"`
	Components []BetterStackComponent   `json:"components"`
}

type BetterStackIncident struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Body     string `json:"body"`
	Status   string `json:"status"`
	Impact   string `json:"impact"`
	URL      string `json:"url"`
	StartAt  string `json:"started_at"`
	EndAt    string `json:"ended_at"`
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

	data, err := a.fetch(ctx, endpoint)
	if err != nil {
		return Result{}, err
	}

	var resp BetterStackResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse BetterStack response: %w", err)
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
			StartedAt: parseTime(inc.StartAt),
			ResolvedAt: parseTime(inc.EndAt),
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

func (a *BetterStackAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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
