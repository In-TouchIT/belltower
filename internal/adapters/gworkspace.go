package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GWorkspaceAdapter handles Google Workspace status
// Endpoint: https://www.google.com/appsstatus/dashboard/incidents.json
type GWorkspaceAdapter struct {
	client *http.Client
	ua     string
}

type GWorkspaceResponse struct {
	Incidents []GWorkspaceIncident `json:"incidents"`
}

type GWorkspaceIncident struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	URL       string `json:"url"`
	Services  []GWorkspaceService `json:"services"`
}

type GWorkspaceService struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func NewGWorkspaceAdapter(client *http.Client, ua string) *GWorkspaceAdapter {
	return &GWorkspaceAdapter{client: client, ua: ua}
}

func (a *GWorkspaceAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	data, err := a.fetch(ctx, "https://www.google.com/appsstatus/dashboard/incidents.json")
	if err != nil {
		return Result{}, err
	}

	var resp GWorkspaceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse GWorkspace response: %w", err)
	}

	result := Result{
		Indicator: IndicatorNone,
	}

	for _, inc := range resp.Incidents {
		isOpen := inc.Status != "closed" && inc.Status != "resolved"
		if isOpen {
			result.Indicator = IndicatorMajor
		}
		
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:       inc.Body,
			Status:     inc.Status,
			Impact:     inc.Impact,
			StartedAt:  parseTime(inc.StartTime),
			ResolvedAt: parseTime(inc.EndTime),
			URL:        inc.URL,
			RawJSON:    string(data),
		})

		for _, svc := range inc.Services {
		result.Components = append(result.Components, Component{
			Name:   svc.Name,
			Status: svc.Status,
		})
	}
	}

	return result, nil
}

func (a *GWorkspaceAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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
