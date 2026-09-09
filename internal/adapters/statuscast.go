package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// StatusCastAdapter handles status pages hosted on the StatusCast platform
// (used by Fastly, SonicWall, and others).
//
// StatusCast exposes a JSON API at /api/v1/:
// - GET /api/v1/components - hierarchical component tree with nested children
// - GET /api/v1/status - page-level health status
// - GET /api/v1/incidents - incident reports (returns 404 for many pages)
type StatusCastAdapter struct {
	client *http.Client
	ua     string
}

// StatusCastComponent represents a component in the StatusCast API
type StatusCastComponent struct {
	ID             int                    `json:"id"`
	Name           string                 `json:"name"`
	Description    string                 `json:"description"`
	Priority       int                    `json:"priority"`
	ParentID       int                    `json:"parentId"`
	Children       []StatusCastComponent  `json:"children"`
	State          *StatusCastState       `json:"state,omitempty"`
	StateText      string                 `json:"stateText,omitempty"`
}

// StatusCastState represents the component state
type StatusCastState struct {
	Normal     bool   `json:"normal"`
	Status     string `json:"status"`     // operational, performance_issue, partial_outage, full_outage
	StatusCode int    `json:"statusCode"` // numeric status code
}

// StatusCastStatus represents the page-level status response
type StatusCastStatus struct {
	State *StatusCastState `json:"state"`
}

// NewStatusCastAdapter creates a StatusCast status adapter.
func NewStatusCastAdapter(client *http.Client, ua string) *StatusCastAdapter {
	return &StatusCastAdapter{client: client, ua: ua}
}

// Fetch implements the Adapter interface for StatusCast status pages.
func (a *StatusCastAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	baseURL := strings.TrimSuffix(p.Endpoint, "/")

	result := Result{
		Indicator: IndicatorUnknown,
	}

	// Get components
	components, compErr := a.fetchComponents(ctx, baseURL+"/components")
	if compErr != nil {
		return result, fmt.Errorf("failed to fetch StatusCast components: %w", compErr)
	}

	result.Components = components
	result.HTTPStatus = 200

	// Derive indicator from components
	if len(components) > 0 {
		result.Indicator = a.calculateIndicator(components)
	} else {
		result.Indicator = IndicatorNone // No components with issues
	}

	// Try to get incidents (may not exist on all StatusCast pages)
	if incidents, err := a.fetchIncidents(ctx, baseURL); err == nil && len(incidents) > 0 {
		result.Incidents = incidents
		if result.Indicator == IndicatorNone {
			result.Indicator = IndicatorMinor
		}
	}

	return result, nil
}

func (a *StatusCastAdapter) fetchComponents(ctx context.Context, url string) ([]Component, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	var components []StatusCastComponent
	if err := json.Unmarshal(httpResp.Body, &components); err != nil {
		return nil, fmt.Errorf("failed to parse StatusCast components: %w", err)
	}

	// Flatten and filter
	flat := a.flattenComponents(components)
	var result []Component
	for _, c := range flat {
		if c.Status == "" || c.Status == "operational" {
			continue // Only report non-operational components
		}
		result = append(result, c)
	}

	// If no components were returned, return an empty slice (not nil)
	// so the caller knows the fetch succeeded
	return result, nil
}

// flattenComponents recursively flattens the component tree
func (a *StatusCastAdapter) flattenComponents(components []StatusCastComponent) []Component {
	var result []Component
	for _, c := range components {
		// Recursively process children first
		if len(c.Children) > 0 {
			result = append(result, a.flattenComponents(c.Children)...)
			continue
		}

		// Leaf component - include its state
		status := "operational"
		if c.State != nil {
			status = c.State.Status
		} else if c.StateText != "" {
			status = strings.ToLower(c.StateText)
		}

		result = append(result, Component{
			Name:   c.Name,
			Status: status,
		})
	}
	return result
}

func (a *StatusCastAdapter) calculateIndicator(components []Component) Indicator {
	worst := IndicatorNone
	for _, c := range components {
		switch c.Status {
		case "full_outage":
			return IndicatorCritical
		case "partial_outage", "performance_issue":
			if worst != IndicatorCritical {
				worst = IndicatorMajor
			}
		case "under_maintenance":
			if worst == IndicatorNone {
				worst = IndicatorMaintenance
			}
		}
	}
	return worst
}

func (a *StatusCastAdapter) fetchIncidents(ctx context.Context, baseURL string) ([]Incident, error) {
	// StatusCast incidents endpoint structure varies by installation
	// Try a common pattern
	url := baseURL + "/incidents"
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	// StatusCast incidents format is not standardized; most pages don't
	// support it. Return empty slice since we couldn't parse anything.
	var raw []map[string]interface{}
	if err := json.Unmarshal(httpResp.Body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse StatusCast incidents: %w", err)
	}

	var incidents []Incident
	for _, item := range raw {
		title, _ := item["title"].(string)
		if title == "" {
			continue
		}

		incidents = append(incidents, Incident{
			ExtID: fmt.Sprintf("%v", item["id"]),
			Title: title,
		})
	}

	return incidents, nil
}
