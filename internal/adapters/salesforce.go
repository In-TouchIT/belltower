package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SalesforceAdapter handles Salesforce status
// Endpoint: https://api.status.salesforce.com/v1/incidents
type SalesforceAdapter struct {
	client *http.Client
	ua     string
}

type SalesforceResponse struct {
	Incidents []SalesforceIncident `json:"data"`
}

type SalesforceIncident struct {
	ID        string `json:"id"`
	Title     string `json:"name"`
	Body      string `json:"details"`
	Status    string `json:"status"`
	Severity  string `json:"severity"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	URL       string `json:"url"`
}

func NewSalesforceAdapter(client *http.Client, ua string) *SalesforceAdapter {
	return &SalesforceAdapter{client: client, ua: ua}
}

func (a *SalesforceAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	data, err := a.fetch(ctx, "https://api.status.salesforce.com/v1/incidents")
	if err != nil {
		return Result{}, err
	}

	var resp SalesforceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse Salesforce response: %w", err)
	}

	result := Result{
		Indicator: IndicatorNone,
	}

	for _, inc := range resp.Incidents {
		isOpen := inc.Status != "Resolved" && inc.Status != "Closed"
		if isOpen {
			result.Indicator = a.mapSeverity(inc.Severity)
		}
		
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Title,
			Body:       inc.Body,
			Status:     inc.Status,
			Impact:     inc.Severity,
			StartedAt:  parseTime(inc.StartTime),
			ResolvedAt: parseTime(inc.EndTime),
			URL:        inc.URL,
			RawJSON:    string(data),
		})
	}

	return result, nil
}

func (a *SalesforceAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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

func (a *SalesforceAdapter) mapSeverity(severity string) Indicator {
	switch severity {
	case "Severity 1", "Critical":
		return IndicatorCritical
	case "Severity 2", "Major":
		return IndicatorMajor
	case "Severity 3", "Minor":
		return IndicatorMinor
	default:
		return IndicatorUnknown
	}
}
