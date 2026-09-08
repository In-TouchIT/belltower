package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// WebexAdapter handles Cisco Webex's custom status API at
// https://status.webex.com/index.json
//
// Response format:
//   {
//     "status": {"indicator": "green|yellow|red"},
//     "components": [{"name": "Webex Meetings", "status": "operational"}, ...],
//     "incidents": [],
//     "scheduled_maintenances": []
//   }
type WebexAdapter struct {
	client *http.Client
	ua     string
}

type webexStatusDoc struct {
	Status   webexOverallStatus `json:"status"`
	Services []webexComponent   `json:"components"`
}

type webexOverallStatus struct {
	Indicator string `json:"indicator"` // green, yellow, red
}

type webexComponent struct {
	Name   string `json:"name"`
	Status string `json:"status"` // operational, under_maintenance, etc.
}

// NewWebexAdapter creates a Webex status adapter.
func NewWebexAdapter(client *http.Client, ua string) *WebexAdapter {
	return &WebexAdapter{client: client, ua: ua}
}

// Fetch implements the Adapter interface for Webex status.
func (a *WebexAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for Webex provider %s", p.Name)
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to fetch Webex status: %w", err)
	}
	data := httpResp.Body

	var doc webexStatusDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse Webex status JSON: %w", err)
	}

	result := Result{
		HTTPStatus: httpResp.StatusCode,
	}

	// Map the overall indicator
	switch strings.ToLower(doc.Status.Indicator) {
	case "green", "none":
		result.Indicator = IndicatorNone
	case "yellow":
		result.Indicator = IndicatorMinor
	case "red":
		result.Indicator = IndicatorMajor
	default:
		result.Indicator = IndicatorNone
	}

	// Map component statuses
	for _, svc := range doc.Services {
		if svc.Status != "operational" {
			result.Components = append(result.Components, Component{
				Name:   svc.Name,
				Status: svc.Status,
			})
		}
	}

	return result, nil
}
