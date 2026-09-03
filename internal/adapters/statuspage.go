package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// StatusPageAdapter handles StatusPage.io instances
// These follow the pattern: <base>/api/v2/summary.json
// Most providers (128+) use this adapter
type StatusPageAdapter struct {
	client *http.Client
	ua     string // User-Agent header
}

// StatusPageSummary is the summary JSON endpoint structure
type StatusPageSummary struct {
	Page struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		URL       string `json:"url"`
		TimeZone  string `json:"time_zone"`
	} `json:"page"`
	ActiveIncidentCount int `json:"active_incident_count"`
	Incidents           []StatusPageIncident `json:"incidents"`
	Components          []StatusPageComponent `json:"components"`
}

type StatusPageIncident struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Status     string `json:"status"`
	Impact     string `json:"impact"`
	URL        string `json:"shortlink"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type StatusPageComponent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// NewStatusPageAdapter creates a new StatusPage adapter
func NewStatusPageAdapter(httpClient *http.Client, userAgent string) *StatusPageAdapter {
	return &StatusPageAdapter{
		client: httpClient,
		ua:     userAgent,
	}
}

// Fetch retrieves status from a StatusPage.io instance
func (a *StatusPageAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	// Determine the summary endpoint
	endpoint := p.Endpoint
	if endpoint == "" {
		// Derive from page URL: <base>/api/v2/summary.json
		base := p.PageURL
		if base == "" {
			return Result{}, fmt.Errorf("no endpoint or page URL for %s", p.ID)
		}
		endpoint = base + "/api/v2/summary.json"
	}

	// Make the request
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return Result{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Result{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var summary StatusPageSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return Result{}, fmt.Errorf("failed to decode response: %w", err)
	}

	result := Result{}

	// Determine overall indicator based on components and incidents
	result.Indicator = a.computeIndicator(summary)

	// Convert incidents
	for _, inc := range summary.Incidents {
		incident := a.parseIncident(inc)
		result.Incidents = append(result.Incidents, incident)
	}

	// Convert components
	for _, comp := range summary.Components {
		component := Component{
			Name:      comp.Name,
			Status:    a.normalizeStatus(comp.Status),
			UpdatedAt: parseTime(comp.UpdatedAt),
		}
		result.Components = append(result.Components, component)
	}

	return result, nil
}

// computeIndicator determines the overall indicator from components and incidents
func (a *StatusPageAdapter) computeIndicator(summary StatusPageSummary) Indicator {
	// Check if any unresolved incidents exist
	hasMajor := false
	hasCritical := false
	for _, inc := range summary.Incidents {
		if inc.Status != "closed" && inc.Status != "resolved" {
			switch inc.Impact {
			case "major":
				hasMajor = true
			case "critical":
				hasCritical = true
			}
		}
	}

	if hasCritical {
		return IndicatorCritical
	}
	if hasMajor {
		return IndicatorMajor
	}

	// Check component statuses
	for _, comp := range summary.Components {
		status := a.normalizeStatus(comp.Status)
		switch status {
		case "major_outage", "critical":
			return IndicatorCritical
		case "partial_outage", "major":
			hasMajor = true
		case "degraded_performance", "degraded", "minor":
			// degraded performance doesn't escalate overall indicator
		case "maintenance":
			return IndicatorMaintenance
		}
	}

	if hasMajor {
		return IndicatorMajor
	}

	return IndicatorNone
}

// parseIncident converts a StatusPage incident to our Incident type
func (a *StatusPageAdapter) parseIncident(inc StatusPageIncident) Incident {
	var startedAt, resolvedAt time.Time

	if inc.CreatedAt != "" {
		startedAt = parseTime(inc.CreatedAt)
	}
	if inc.UpdatedAt != "" && inc.Status == "closed" {
		resolvedAt = parseTime(inc.UpdatedAt)
	}

	return Incident{
		ExtID:     inc.ID,
		Title:     inc.Name,
		Body:      inc.Body,
		Status:    inc.Status,
		Impact:    inc.Impact,
		StartedAt: startedAt,
		ResolvedAt: resolvedAt,
		URL:       inc.URL,
	}
}

// normalizeStatus converts StatusPage component statuses to a standardized format
func (a *StatusPageAdapter) normalizeStatus(status string) string {
	switch status {
	case "operational":
		return "operational"
	case "degraded_performance":
		return "degraded_performance"
	case "partial_outage":
		return "partial_outage"
	case "major_outage":
		return "major_outage"
	case "under_maintenance":
		return "maintenance"
	default:
		return status
	}
}

// parseTime parses an ISO 8601 timestamp
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try other common formats
		t, err = time.Parse("2006-01-02T15:04:05Z", s)
		if err != nil {
			return time.Now()
		}
	}
	return t
}

// LoadProvidersFromYAML loads providers from a YAML file
func LoadProvidersFromYAML(path string) ([]ProviderInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read providers file: %w", err)
	}

	var result struct {
		Providers []struct {
			ID       string `yaml:"id"`
			Name     string `yaml:"name"`
			Category string `yaml:"category"`
			PageURL  string `yaml:"page_url"`
			Adapter  string `yaml:"adapter"`
			Endpoint string `yaml:"endpoint"`
			Tier     int    `yaml:"tier"`
			Enabled  bool   `yaml:"enabled"`
			Notes    string `yaml:"notes"`
		} `yaml:"providers"`
	}

	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	var providers []ProviderInfo
	for _, p := range result.Providers {
		if !p.Enabled {
			continue
		}
		providers = append(providers, ProviderInfo{
			ID:       p.ID,
			Name:     p.Name,
			Category: p.Category,
			PageURL:  p.PageURL,
			Adapter:  p.Adapter,
			Endpoint: p.Endpoint,
			Tier:     p.Tier,
		})
	}

	return providers, nil
}
