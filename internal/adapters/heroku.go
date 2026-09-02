package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HerokuAdapter handles Heroku status
// Endpoint: https://status.heroku.com/api/v4/current-status
type HerokuAdapter struct {
	client *http.Client
	ua     string
}

type HerokuResponse struct {
	Status struct {
		Indicator string `json:"indicator"`
	} `json:"status"`
	Incidents []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		Status    string `json:"status"`
		Severity  string `json:"severity"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	} `json:"incidents"`
	Components []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"components"`
}

func NewHerokuAdapter(client *http.Client, ua string) *HerokuAdapter {
	return &HerokuAdapter{client: client, ua: ua}
}

func (a *HerokuAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	data, err := a.fetch(ctx, "https://status.heroku.com/api/v4/current-status")
	if err != nil {
		return Result{}, err
	}

	var resp HerokuResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse Heroku response: %w", err)
	}

	result := Result{
		Indicator: a.mapIndicator(resp.Status.Indicator),
	}

	for _, inc := range resp.Incidents {
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:       inc.Body,
			Status:     inc.Status,
			Impact:     inc.Severity,
			StartedAt:  parseTime(inc.CreatedAt),
			ResolvedAt: parseTime(inc.UpdatedAt),
			RawJSON:    string(data),
		})
	}

	for _, comp := range resp.Components {
		result.Components = append(result.Components, Component{
			Name:   comp.Name,
			Status: comp.Status,
		})
	}

	return result, nil
}

func (a *HerokuAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.ua)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (a *HerokuAdapter) mapIndicator(indicator string) Indicator {
	switch indicator {
	case "none":
		return IndicatorNone
	case "minor":
		return IndicatorMinor
	case "major":
		return IndicatorMajor
	case "critical":
		return IndicatorCritical
	case "maintenance":
		return IndicatorMaintenance
	default:
		return IndicatorUnknown
	}
}
