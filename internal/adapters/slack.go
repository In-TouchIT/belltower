package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SlackAdapter handles Slack status
// Endpoint: https://slack-status.com/api/v2.0.0/current
type SlackAdapter struct {
	client *http.Client
	ua     string
}

type SlackResponse struct {
	ActiveIncidents []SlackIncident `json:"active_incidents"`
	ActiveMaintenance []SlackIncident `json:"active_maintenance"`
}

type SlackIncident struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Severity  string `json:"severity"`
	StartedAt string `json:"started_at"`
	EndAt     string `json:"end_at"`
	URL       string `json:"url"`
}

func NewSlackAdapter(client *http.Client, ua string) *SlackAdapter {
	return &SlackAdapter{client: client, ua: ua}
}

func (a *SlackAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	data, err := a.fetch(ctx, "https://slack-status.com/api/v2.0.0/current")
	if err != nil {
		return Result{}, err
	}

	var resp SlackResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse Slack response: %w", err)
	}

	result := Result{
		Indicator: IndicatorNone,
	}

	for _, inc := range resp.ActiveIncidents {
		result.Indicator = IndicatorMajor
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:        inc.Body,
			Status:     "open",
			Impact:     inc.Severity,
			StartedAt:  parseTime(inc.StartedAt),
			URL:        inc.URL,
			RawJSON:    string(data),
		})
	}

	for _, inc := range resp.ActiveMaintenance {
		result.Indicator = IndicatorMaintenance
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      "MAINTENANCE: " + inc.Title,
			Body:        inc.Body,
			Status:     "maintenance",
			Impact:     "maintenance",
			StartedAt:  parseTime(inc.StartedAt),
			URL:        inc.URL,
			RawJSON:    string(data),
		})
	}

	return result, nil
}

func (a *SlackAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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
