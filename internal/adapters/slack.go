package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// SlackAdapter handles Slack status
// Endpoint: https://slack-status.com/api/v2.0.0/current
type SlackAdapter struct {
	client *http.Client
	ua     string
}

const slackCurrentURL = "https://slack-status.com/api/v2.0.0/current"

type SlackResponse struct {
	ActiveIncidents   []SlackIncident `json:"active_incidents"`
	ActiveMaintenance []SlackIncident `json:"active_maintenance"`
}

type SlackIncident struct {
	ID        json.Number `json:"id"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	Status    string      `json:"status"`
	Severity  string      `json:"severity"`
	StartedAt string      `json:"started_at"`
	EndAt     string      `json:"end_at"`
	URL       string      `json:"url"`
}

func NewSlackAdapter(client *http.Client, ua string) *SlackAdapter {
	return &SlackAdapter{client: client, ua: ua}
}

func (a *SlackAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = slackCurrentURL
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	var resp SlackResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse Slack response: %w", err)
	}

	result := Result{
		Indicator:  IndicatorNone,
		HTTPStatus: httpResp.StatusCode,
	}

	for _, inc := range resp.ActiveIncidents {
		result.Indicator = IndicatorMajor
		result.Incidents = append(result.Incidents, Incident{
			ExtID:     inc.ID.String(),
			Title:     inc.Title,
			Body:      inc.Body,
			Status:    StatusOpen,
			Impact:    inc.Severity,
			StartedAt: parseTime(inc.StartedAt),
			URL:       inc.URL,
			RawJSON:   string(data),
		})
	}

	for _, inc := range resp.ActiveMaintenance {
		result.Indicator = IndicatorMaintenance
		result.Incidents = append(result.Incidents, Incident{
			ExtID:     inc.ID.String(),
			Title:     "MAINTENANCE: " + inc.Title,
			Body:      inc.Body,
			Status:    StatusMaintenance,
			Impact:    "maintenance",
			StartedAt: parseTime(inc.StartedAt),
			URL:       inc.URL,
			RawJSON:   string(data),
		})
	}

	return result, nil
}
