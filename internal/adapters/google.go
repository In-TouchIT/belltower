package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Google publishes Cloud and Workspace status on the same incidents.json
// schema: begin/end/external_desc/status_impact/severity/uri/affected_products.
//
// Both adapters previously looked for title/content/status/start_time, none of
// which exist in that schema, so every Google incident was stored with an empty
// title (and, for Workspace, no impact).
type googleIncident struct {
	ID               string `json:"id"`
	Number           string `json:"number"`
	Begin            string `json:"begin"`
	End              string `json:"end"`
	Created          string `json:"created"`
	Modified         string `json:"modified"`
	ExternalDesc     string `json:"external_desc"`
	Severity         string `json:"severity"`
	StatusImpact     string `json:"status_impact"`
	ServiceName      string `json:"service_name"`
	URI              string `json:"uri"`
	MostRecentUpdate struct {
		Text   string `json:"text"`
		When   string `json:"when"`
		Status string `json:"status"`
	} `json:"most_recent_update"`
	AffectedProducts []struct {
		Title string `json:"title"`
		ID    string `json:"id"`
	} `json:"affected_products"`
}

// fetchGoogleStatus retrieves and parses a Google status dashboard feed.
// baseURL is the dashboard root used to absolutize each incident's uri.
func fetchGoogleStatus(ctx context.Context, client *http.Client, ua, endpoint, baseURL string) (Result, error) {
	resp, err := httpGet(ctx, client, ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: resp.StatusCode}, err
	}

	var incidents []googleIncident
	if err := json.Unmarshal(resp.Body, &incidents); err != nil {
		return Result{HTTPStatus: resp.StatusCode}, fmt.Errorf("failed to parse Google status response: %w", err)
	}

	result := Result{Indicator: IndicatorNone, HTTPStatus: resp.StatusCode}
	worst := gcpNone

	for _, inc := range incidents {
		// incidents.json is a rolling history, so an end timestamp is what
		// separates a past incident from a live one.
		resolved := inc.End != ""

		status := StatusResolved
		if !resolved {
			status = StatusOpen
			if sev := gcpIndicator(inc.StatusImpact); sev > worst {
				worst = sev
			}
		}

		// external_desc is the human-readable summary and the only title-like
		// field the schema offers.
		title := inc.ExternalDesc
		if title == "" {
			title = inc.ServiceName
		}

		body := inc.MostRecentUpdate.Text
		if body == "" {
			body = inc.ExternalDesc
		}

		url := ""
		if inc.URI != "" {
			url = baseURL + inc.URI
		}

		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      title,
			Body:       body,
			Status:     status,
			Impact:     gcpImpact(inc.StatusImpact, inc.Severity),
			StartedAt:  parseTime(inc.Begin),
			ResolvedAt: parseTime(inc.End),
			URL:        url,
		})

		// Only a live incident says anything about current component health.
		if resolved {
			continue
		}
		for _, prod := range inc.AffectedProducts {
			result.Components = append(result.Components, Component{
				Name:   prod.Title,
				Status: gcpComponentStatus(inc.StatusImpact),
			})
		}
	}

	result.Indicator = worst.indicator()
	return result, nil
}
