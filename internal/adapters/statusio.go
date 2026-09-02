package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// StatusIOAdapter handles Status.io status pages
// Pattern: https://api.status.io/1.0/status/<pageID>
type StatusIOAdapter struct {
	client *http.Client
	ua     string
}

type StatusIOResponse struct {
	Page struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		TimeZone string `json:"time_zone"`
	} `json:"page"`
	Status struct {
		Indicator   string `json:"indicator"`
		Description string `json:"description"`
	} `json:"status"`
	Incidents  []StatusIOIncident  `json:"incidents"`
	Components []StatusIOComponent `json:"components"`
}

type StatusIOIncident struct {
	ID        string `json:"_id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
	Link      string `json:"link"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type StatusIOComponent struct {
	ID        string `json:"_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

func NewStatusIOAdapter(client *http.Client, ua string) *StatusIOAdapter {
	return &StatusIOAdapter{client: client, ua: ua}
}

func (a *StatusIOAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for status.io provider %s", p.Name)
	}

	data, err := a.fetch(ctx, endpoint)
	if err != nil {
		return Result{}, err
	}

	var resp StatusIOResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{}, fmt.Errorf("failed to parse status.io response: %w", err)
	}

	result := Result{
		Indicator: a.mapIndicator(resp.Status.Indicator),
	}

	for _, inc := range resp.Incidents {
		result.Incidents = append(result.Incidents, Incident{
			ExtID:      inc.ID,
			Title:      inc.Name,
			Body:       inc.Body,
			Status:     inc.Status,
			Impact:     inc.Impact,
			StartedAt:  parseTime(inc.CreatedAt),
			ResolvedAt: parseTime(inc.UpdatedAt),
			URL:        inc.Link,
			RawJSON:    string(data),
		})
	}

	for _, comp := range resp.Components {
		result.Components = append(result.Components, Component{
			Name:      comp.Name,
			Status:    comp.Status,
			UpdatedAt: parseTime(comp.UpdatedAt),
		})
	}

	return result, nil
}

func (a *StatusIOAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
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

func (a *StatusIOAdapter) mapIndicator(indicator string) Indicator {
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
