package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GCPAdapter handles Google Cloud Platform status
// Endpoint: https://status.cloud.google.com/incidents.json
type GCPAdapter struct {
	client *http.Client
	ua     string
}

type GCPResponse struct {
	Incidents []GCPIncident `json:"incidents"`
}

type GCPIncident struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Content   string       `json:"content"`
	Status    string       `json:"status"`
	Severity  string       `json:"severity"`
	StartTime string       `json:"start_time"`
	EndTime   string       `json:"end_time"`
	URL       string       `json:"url"`
	Services  []GCPService `json:"services"`
}

type GCPService struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func NewGCPAdapter(client *http.Client, ua string) *GCPAdapter {
	return &GCPAdapter{client: client, ua: ua}
}

func (a *GCPAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	data, err := a.fetch(ctx, "https://status.cloud.google.com/incidents.json")
	if err != nil {
		return Result{}, err
	}

	var resp GCPResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse GCP response: %w", err)
	}

	result := Result{
		Indicator: IndicatorNone,
	}

	for _, inc := range resp.Incidents {
		status := "open"
		if inc.Status == "Closed" {
			status = "resolved"
		}

		isOpen := status == "open"
		if isOpen {
			result.Indicator = IndicatorMajor
		}

		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:       inc.Content,
			Status:     status,
			Impact:     inc.Severity,
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

func (a *GCPAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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
