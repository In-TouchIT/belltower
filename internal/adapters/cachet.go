package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CachetAdapter handles status pages running the Cachet open-source platform
// (used by CyberArk, ColoBlxs, Servosity, etc.).
//
// Cachet exposes a REST API with:
// - GET /api/v1/status       - overall page status
// - GET /api/v1/components   - component list (may be nested)
// - GET /api/v1/notices      - incidents/incident reports
//
// Component states: 0=operational, 1=performance_issues, 2=partial_outage, 3=major_outage
// Notice types: incident, planned, technical
// Notice states: scheduled, in_progress, verifying, resolved, closed
type CachetAdapter struct {
	client *http.Client
	ua     string
}

// CachetStatus represents the overall page status response
type CachetStatus struct {
	Page struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		State     int    `json:"state"`      // 0=operational, 1=have_issues, 2=major_outage
		StateText string `json:"state_text"` // human-readable status text
		URL       string `json:"url"`
		UpdatedAt string `json:"updated_at"`
	} `json:"page"`
}

// CachetComponent represents a component in the Cachet API
type CachetComponent struct {
	ID          int                `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Status      int                `json:"status"`
	URL         string             `json:"url"`
	ParentID    int                `json:"parent_id"`
	Children    []CachetComponent  `json:"children"`
	UpdatedAt   string             `json:"updated_at"`
}

// CachetNotice represents an incident/notice in the Cachet API
type CachetNotice struct {
	ID            int    `json:"id"`
	Type          string `json:"type"`
	State         string `json:"state"`
	Subject       string `json:"subject"`
	URL           string `json:"url"`
	BeginsAt      string `json:"begins_at"`
	EndsAt        string `json:"ends_at"`
	BeganAt       string `json:"began_at"`
	EndedAt       string `json:"ended_at"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	TimelineState string `json:"timeline_state"`
	LatestUpdate  struct {
		State   string `json:"state"`
		Content string `json:"content"`
	} `json:"latest_update"`
}

// cachetComponentsResponse wraps the components array
type cachetComponentsResponse struct {
	Components []CachetComponent `json:"components"`
}

// cachetNoticesResponse wraps the notices array
type cachetNoticesResponse struct {
	Notices []CachetNotice `json:"notices"`
}

// NewCachetAdapter creates a Cachet status adapter.
func NewCachetAdapter(client *http.Client, ua string) *CachetAdapter {
	return &CachetAdapter{client: client, ua: ua}
}

// Fetch implements the Adapter interface for Cachet status pages.
func (a *CachetAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	baseURL := strings.TrimSuffix(p.Endpoint, "/")

	result := Result{
		Indicator: IndicatorUnknown,
	}

	// Get overall status
	status, statusErr := a.fetchStatus(ctx, baseURL+"/status")
	if statusErr == nil {
		result.HTTPStatus = 200
		// Map Cachet page state to indicator
		switch status.Page.State {
		case 0:
			result.Indicator = IndicatorNone
		case 1:
			result.Indicator = IndicatorMinor
		case 2, 3:
			result.Indicator = IndicatorMajor
		}
	}

	// Get raw components for status calculation
	rawComponents, compErr := a.fetchRawComponents(ctx, baseURL+"/components")
	if compErr != nil {
		return result, fmt.Errorf("failed to fetch Cachet components: %w", compErr)
	}

	// Derive indicator from raw components
	if len(rawComponents) > 0 {
		hasMajor := false
		hasMinor := false
		for _, c := range rawComponents {
			switch c.Status {
			case 3, 4: // major outage
				hasMajor = true
			case 1, 2: // performance issues or partial outage
				hasMinor = true
			}
		}
		if hasMajor {
			result.Indicator = IndicatorMajor
		} else if hasMinor {
			result.Indicator = IndicatorMinor
		} else {
			result.Indicator = IndicatorNone
		}
	}

	// Convert to our Component format
	result.Components = a.flattenComponents(rawComponents)

	// Get incidents (notices)
	notices, noticeErr := a.fetchIncidents(ctx, baseURL+"/notices")
	if noticeErr != nil {
		// Try /incidents endpoint (ColoBlxs variant)
		notices, noticeErr = a.fetchIncidents(ctx, baseURL+"/incidents")
	}
	if noticeErr == nil {
		result.Incidents = notices
		// If we found any active incidents, ensure indicator reflects issues
		if len(notices) > 0 && result.Indicator == IndicatorNone {
			result.Indicator = IndicatorMinor
		}
	}

	return result, nil
}

// fetchStatus gets the overall page status (handles multiple Cachet variants)
func (a *CachetAdapter) fetchStatus(ctx context.Context, url string) (*CachetStatus, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	// Try standard Cachet format
	var status CachetStatus
	if err := json.Unmarshal(httpResp.Body, &status); err == nil && status.Page.Name != "" {
		return &status, nil
	}

	// Try wrapped format (ColoBlxs variant: {"data": {"status": "success"}})
	var wrapped struct {
		Data struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(httpResp.Body, &wrapped); err == nil && wrapped.Data.Status != "" {
		// Map "success" to operational
		state := 0
		if strings.Contains(strings.ToLower(wrapped.Data.Message), "operational") {
			state = 0
		} else {
			state = 1 // assume issues
		}
		return &CachetStatus{
			Page: struct {
				ID        int    `json:"id"`
				Name      string `json:"name"`
				State     int    `json:"state"`
				StateText string `json:"state_text"`
				URL       string `json:"url"`
				UpdatedAt string `json:"updated_at"`
			}{
				State:     state,
				StateText: wrapped.Data.Message,
			},
		}, nil
	}

	return nil, fmt.Errorf("unable to parse Cachet status response")
}

// fetchRawComponents returns raw Cachet components with numeric status
func (a *CachetAdapter) fetchRawComponents(ctx context.Context, url string) ([]CachetComponent, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	// Try direct array format (CyberArk)
	var components []CachetComponent
	if err := json.Unmarshal(httpResp.Body, &components); err == nil && len(components) > 0 {
		return components, nil
	}

	// Try wrapped format (ColoBlxs: {"data": [...]})
	var wrapped struct {
		Data []CachetComponent `json:"data"`
	}
	if err := json.Unmarshal(httpResp.Body, &wrapped); err == nil && len(wrapped.Data) > 0 {
		return wrapped.Data, nil
	}

	// Try standard Cachet format ({"components": [...]})
	var resp cachetComponentsResponse
	if err := json.Unmarshal(httpResp.Body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse Cachet components: %w", err)
	}

	return resp.Components, nil
}

// flattenComponents recursively flattens nested Cachet components
func (a *CachetAdapter) flattenComponents(components []CachetComponent) []Component {
	var result []Component
	for _, c := range components {
		if len(c.Children) > 0 {
			result = append(result, a.flattenComponents(c.Children)...)
			continue
		}

		result = append(result, Component{
			Name:   c.Name,
			Status: a.mapCachetStatus(c.Status),
		})
	}
	return result
}

// fetchIncidents fetches incidents (called "notices" in standard Cachet,
// "incidents" in some variants)
func (a *CachetAdapter) fetchIncidents(ctx context.Context, url string) ([]Incident, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	body := httpResp.Body

	// Try standard Cachet notices format
	var noticesResp cachetNoticesResponse
	if err := json.Unmarshal(body, &noticesResp); err == nil && len(noticesResp.Notices) > 0 {
		return a.parseNotices(noticesResp.Notices), nil
	}

	// Try incidents format (with data wrapper) - ColoBlxs variant
	var incidentsResp struct {
		Data      []CachetIncident `json:"data"`
		Incidents []CachetIncident `json:"incidents"`
	}
	if err := json.Unmarshal(body, &incidentsResp); err == nil {
		if len(incidentsResp.Data) > 0 {
			return a.parseIncidents(incidentsResp.Data), nil
		}
		if len(incidentsResp.Incidents) > 0 {
			return a.parseIncidents(incidentsResp.Incidents), nil
		}
	}

	// Try direct array
	var directNotices []CachetNotice
	if err := json.Unmarshal(body, &directNotices); err == nil && len(directNotices) > 0 {
		return a.parseNotices(directNotices), nil
	}

	// Try direct incidents array
	var directIncidents []CachetIncident
	if err := json.Unmarshal(body, &directIncidents); err == nil && len(directIncidents) > 0 {
		return a.parseIncidents(directIncidents), nil
	}

	return nil, fmt.Errorf("unrecognized response format")
}

// cachetIncidentsResponse wraps incidents in a data field (ColoBlxs variant)
type cachetIncidentsResponse struct {
	Data []CachetIncident `json:"data"`
}

// CachetIncident represents an incident in the alternative Cachet format
type CachetIncident struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Status      int    `json:"status"`       // 0=operational, 4=resolved
	Message     string `json:"message"`
	OccurredAt  string `json:"occurred_at"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	IsResolved  bool   `json:"is_resolved"`
}

func (a *CachetAdapter) parseNotices(notices []CachetNotice) []Incident {
	var incidents []Incident
	for _, notice := range notices {
		if notice.State == "resolved" || notice.State == "closed" {
			continue
		}

		impact := "minor"
		switch strings.ToLower(notice.Type) {
		case "incident":
			impact = "major"
		case "technical":
			impact = "major"
		case "planned":
			impact = "maintenance"
		}

		body := notice.LatestUpdate.Content
		if body == "" {
			body = notice.Subject
		}

		incidents = append(incidents, Incident{
			ExtID:      fmt.Sprintf("cachet-%d", notice.ID),
			Title:      notice.Subject,
			Impact:     impact,
			Status:     a.mapCachetNoticeState(notice.State),
			Body:       body,
			StartedAt:  parseTime(notice.BeganAt),
			ResolvedAt: parseTime(notice.EndedAt),
			URL:        notice.URL,
		})
	}
	return incidents
}

func (a *CachetAdapter) parseIncidents(incidents []CachetIncident) []Incident {
	var result []Incident
	for _, inc := range incidents {
		if inc.IsResolved {
			continue
		}

		impact := "minor"
		switch inc.Status {
		case 2, 3, 4:
			impact = "major"
		}

		result = append(result, Incident{
			ExtID:      fmt.Sprintf("cachet-inc-%d", inc.ID),
			Title:      inc.Name,
			Impact:     impact,
			Status:     a.mapCachetIncidentStatus(inc.Status),
			Body:       inc.Message,
			StartedAt:  parseTime(inc.OccurredAt),
			URL:        "",
		})
	}
	return result
}

func (a *CachetAdapter) mapCachetStatus(status int) string {
	switch status {
	case 0:
		return "operational"
	case 1:
		return "performance_issues"
	case 2:
		return "partial_outage"
	case 3:
		return "major_outage"
	case 4:
		return "major_outage"
	case 5:
		return "under_maintenance"
	default:
		return "operational"
	}
}

func (a *CachetAdapter) mapCachetNoticeState(state string) string {
	switch strings.ToLower(state) {
	case "scheduled":
		return "scheduled"
	case "in_progress", "verifying":
		return "investigating"
	case "resolved":
		return "resolved"
	case "closed":
		return "resolved"
	default:
		return "open"
	}
}

func (a *CachetAdapter) mapCachetIncidentStatus(status int) string {
	switch status {
	case 0:
		return "open"
	case 1:
		return "investigating"
	case 2:
		return "identified"
	case 3:
		return "monitoring"
	case 4:
		return "resolved"
	default:
		return "open"
	}
}
