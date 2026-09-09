package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// DocuSignStatusAdapter handles DocuSign's custom JSON status API.
// It monitors components and incidents from DocuSign's health endpoints:
//   - Components: https://health.docusign.com/production/1ds/ssg/apps/health/dynamic/components.json
//   - Incidents:  https://health.docusign.com/production/1ds/ssg/apps/health/dynamic/incidents.json
type DocuSignStatusAdapter struct {
	client *http.Client
	ua     string
}

// DocuSignComponent represents a component in the DocuSign status API.
type DocuSignComponent struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Status   string   `json:"status"`
	ParentID string   `json:"parentId"`
	Children []string `json:"children"`
}

// DocuSignIncident represents an incident in the DocuSign status API.
type DocuSignIncident struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Status     string          `json:"status"`
	Impact     string          `json:"impact"`
	StartedAt  string          `json:"startedAt"`
	ResolvedAt string          `json:"resolvedAt"`
	Events     []DocuSignEvent `json:"events"`
}

// DocuSignEvent represents an event within an incident.
type DocuSignEvent struct {
	Body    string `json:"body"`
	Status  string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

// DocuSignComponentsResponse wraps the components list.
type DocuSignComponentsResponse struct {
	Components []DocuSignComponent `json:"components"`
}

// NewDocuSignStatusAdapter creates a DocuSign status adapter.
func NewDocuSignStatusAdapter(client *http.Client, ua string) *DocuSignStatusAdapter {
	return &DocuSignStatusAdapter{client: client, ua: ua}
}

// Fetch implements the Adapter interface for DocuSign status.
func (a *DocuSignStatusAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	// Endpoint should be the base URL (e.g. https://health.docusign.com/production/1ds/ssg/apps/health/dynamic/)
	baseURL := strings.TrimSuffix(p.Endpoint, "/")
	
	// Load components
	components, indicator := a.fetchComponents(ctx, baseURL+"/components.json")
	
	// Load incidents  
	incidents := a.fetchIncidents(ctx, baseURL+"/incidents.json")
	
	return Result{
		Indicator:  indicator,
		Incidents:  incidents,
		Components: components,
		HTTPStatus: 200,
	}, nil
}

func (a *DocuSignStatusAdapter) fetchComponents(ctx context.Context, url string) ([]Component, Indicator) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, IndicatorUnknown
	}
	
	var doc DocuSignComponentsResponse
	if err := json.Unmarshal(httpResp.Body, &doc); err != nil {
		return nil, IndicatorUnknown
	}
	
	comps := make([]Component, 0, len(doc.Components))
	indicator := IndicatorNone
	
	for _, c := range doc.Components {
		comp := Component{
			Name:   c.Name,
			Status: c.Status,
		}
		comps = append(comps, comp)
		
		// Map status to indicator
		switch c.Status {
		case "available":
			// operational
		case "degraded_performance":
			if indicator == IndicatorNone {
				indicator = IndicatorMinor
			}
		case "partial_outage":
			if indicator != IndicatorMajor && indicator != IndicatorCritical {
				indicator = IndicatorMajor
			}
		case "full_outage", "major_outage":
			indicator = IndicatorCritical
		case "under_maintenance":
			if indicator == IndicatorNone {
				indicator = IndicatorMaintenance
			}
		}
	}
	
	return comps, indicator
}

func (a *DocuSignStatusAdapter) fetchIncidents(ctx context.Context, url string) []Incident {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil
	}
	
	var incidentsRaw []DocuSignIncident
	if err := json.Unmarshal(httpResp.Body, &incidentsRaw); err != nil {
		return nil
	}
	
	var incidents []Incident
	for _, inc := range incidentsRaw {
		// Skip resolved incidents
		if strings.EqualFold(inc.Status, "resolved") {
			continue
		}
		
		impact := "minor"
		switch inc.Impact {
		case "full_outage":
			impact = "critical"
		case "partial_outage":
			impact = "major"
		case "performance_degradation":
			impact = "minor"
		}
		
		var body string
		if len(inc.Events) > 0 {
			body = inc.Events[len(inc.Events)-1].Body // latest update
		}
		
		title := inc.Title
		if title == "" {
			title = fmt.Sprintf("DocuSign Incident (ID: %s)", inc.ID)
		}
		
		incidents = append(incidents, Incident{
			ExtID:     inc.ID,
			Title:     title,
			Impact:    impact,
			Status:    NormalizeIncidentStatus(inc.Status),
			Body:      body,
			StartedAt: parseTime(inc.StartedAt),
			URL:       "https://health.docusign.com",
		})
	}
	
	return incidents
}
