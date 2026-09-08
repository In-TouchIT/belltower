package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HerokuAdapter handles Heroku status
// Endpoint: https://status.heroku.com/api/v4/current-status
type HerokuAdapter struct {
	client *http.Client
	ua     string
}

const herokuCurrentStatusURL = "https://status.heroku.com/api/v4/current-status"

type HerokuResponse struct {
	Status []struct {
		System string `json:"system"`
		Status string `json:"status"`
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
	Scheduled []struct {
		ID        json.Number `json:"id"`
		Title     string      `json:"title"`
		State     string      `json:"state"`
		CreatedAt string      `json:"created_at"`
		UpdatedAt string      `json:"updated_at"`
		Resolved  bool        `json:"resolved"`
	} `json:"scheduled"`
	Components []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"components"`
}

func NewHerokuAdapter(client *http.Client, ua string) *HerokuAdapter {
	return &HerokuAdapter{client: client, ua: ua}
}

func (a *HerokuAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = herokuCurrentStatusURL
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	var resp HerokuResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse Heroku response: %w", err)
	}

	// Determine overall indicator based on status colors
	// green = operational, yellow/red = degraded
	indicator := IndicatorNone
	for _, s := range resp.Status {
		switch s.Status {
		case "red":
			indicator = IndicatorMajor
		case "yellow":
			if indicator != IndicatorMajor {
				indicator = IndicatorMinor
			}
		case "green":
			// Operational - no escalation needed
		}
	}

	result := Result{
		Indicator:  indicator,
		HTTPStatus: httpResp.StatusCode,
	}

	for _, inc := range resp.Incidents {
		status := NormalizeIncidentStatus(inc.Status)
		// updated_at is the last activity on the incident; it only doubles
		// as a resolution time once the incident is actually resolved.
		var resolvedAt time.Time
		if status == StatusResolved {
			resolvedAt = parseTime(inc.UpdatedAt)
		}
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:       inc.Body,
			Status:     status,
			Impact:     inc.Severity,
			StartedAt:  parseTime(inc.CreatedAt),
			ResolvedAt: resolvedAt,
			RawJSON:    string(data),
		})
	}

	// Include scheduled maintenances as incidents
	for _, sched := range resp.Scheduled {
		schedStatus := NormalizeIncidentStatus(sched.State)
		if sched.Resolved {
			schedStatus = StatusResolved
		}
		var schedResolvedAt time.Time
		if schedStatus == StatusResolved {
			schedResolvedAt = parseTime(sched.UpdatedAt)
		}
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      sched.ID.String(),
			Title:      sched.Title,
			Status:     schedStatus,
			StartedAt:  parseTime(sched.CreatedAt),
			ResolvedAt: schedResolvedAt,
			URL:        fmt.Sprintf("https://status.heroku.com/incidents/%s", sched.ID.String()),
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
