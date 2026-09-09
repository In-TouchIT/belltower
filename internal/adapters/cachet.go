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
	notices, noticeErr := a.fetchNotices(ctx, baseURL+"/notices")
	if noticeErr == nil {
		result.Incidents = notices
		// If we found any active incidents, ensure indicator reflects issues
		if len(notices) > 0 && result.Indicator == IndicatorNone {
			result.Indicator = IndicatorMinor
		}
	}

	return result, nil
}

// fetchStatus gets the overall page status
func (a *CachetAdapter) fetchStatus(ctx context.Context, url string) (*CachetStatus, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	var status CachetStatus
	if err := json.Unmarshal(httpResp.Body, &status); err != nil {
		return nil, fmt.Errorf("failed to parse Cachet status: %w", err)
	}

	return &status, nil
}

// fetchRawComponents returns raw Cachet components with numeric status
func (a *CachetAdapter) fetchRawComponents(ctx context.Context, url string) ([]CachetComponent, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

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

// fetchNotices fetches incidents (called "notices" in Cachet)
func (a *CachetAdapter) fetchNotices(ctx context.Context, url string) ([]Incident, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "application/json")
	if err != nil {
		return nil, err
	}

	var resp cachetNoticesResponse
	if err := json.Unmarshal(httpResp.Body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse Cachet notices: %w", err)
	}

	var incidents []Incident
	for _, notice := range resp.Notices {
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

	return incidents, nil
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
