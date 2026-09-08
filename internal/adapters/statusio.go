package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// StatusIOAdapter handles Status.io status pages
// Pattern: https://api.status.io/1.0/status/<pageID>
type StatusIOAdapter struct {
	client *http.Client
	ua     string
}

// StatusIOResponse is the actual response structure from the status.io API
type StatusIOResponse struct {
	Result struct {
		Page struct {
			Name        string `json:"name"`
			URL         string `json:"url"`
			TimeZone    string `json:"time_zone"`
			Description string `json:"description"`
		} `json:"page"`
		StatusOverall struct {
			Indicator   string `json:"indicator"`
			Status      string `json:"status"`
			Description string `json:"description"`
			Updated     string `json:"last_updated"`
			StatusCode  int    `json:"status_code"`
		} `json:"status_overall"`
		Components []StatusIOComponent `json:"components"`
		Incidents  []StatusIOIncident  `json:"incidents"`
		Scheduled  []StatusIOScheduled `json:"scheduled_maintenances"`
	} `json:"result"`
	Error string `json:"error"`
}

type StatusIOIncident struct {
	ID         string           `json:"_id"`
	Name       string           `json:"name"`
	Body       string           `json:"body"`
	Status     string           `json:"status"`
	Impact     string           `json:"impact"`
	Link       string           `json:"link"`
	CreatedAt  string           `json:"created_at"`
	UpdatedAt  string           `json:"updated_at"`
	ResolvedAt string           `json:"resolved_at"`
	Components []string         `json:"components"`
	Updates    []StatusIOUpdate `json:"updates"`
}

type StatusIOUpdate struct {
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
}

type StatusIOComponent struct {
	ID          string `json:"_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	StatusCode  int    `json:"status_code"`
	UpdatedAt   string `json:"updated"`
	Description string `json:"description"`
}

type StatusIOScheduled struct {
	ID         string           `json:"_id"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	Link       string           `json:"link"`
	CreatedAt  string           `json:"created_at"`
	UpdatedAt  string           `json:"updated_at"`
	StartsAt   string           `json:"scheduled_start_date"`
	EndsAt     string           `json:"scheduled_end_date"`
	Components []string         `json:"components"`
	Updates    []StatusIOUpdate `json:"updates"`
}

func NewStatusIOAdapter(client *http.Client, ua string) *StatusIOAdapter {
	return &StatusIOAdapter{client: client, ua: ua}
}

func (a *StatusIOAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for status.io provider %s", p.Name)
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	var resp StatusIOResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse status.io response: %w", err)
	}

	if resp.Error != "" || (resp.Result.StatusOverall.StatusCode == 0 && resp.Result.StatusOverall.Status == "") {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("status.io API error: %s", resp.Error)
	}

	// An unrecognized overall status is reported as unknown rather than
	// silently defaulting to "none" (operational).
	result := Result{
		Indicator:  a.mapIndicator(resp.Result.StatusOverall.Status),
		HTTPStatus: httpResp.StatusCode,
	}

	// Map components
	for _, comp := range resp.Result.Components {
		result.Components = append(result.Components, Component{
			Name:      comp.Name,
			Status:    a.normalizeComponentStatus(comp.StatusCode, comp.Status),
			UpdatedAt: parseTime(comp.UpdatedAt),
		})
	}

	// Map incidents
	for _, inc := range resp.Result.Incidents {
		result.Incidents = append(result.Incidents, a.parseIncident(inc))
	}

	// Map scheduled maintenances as incidents with "scheduled" status
	for _, sched := range resp.Result.Scheduled {
		result.Incidents = append(result.Incidents, a.parseScheduled(sched))
	}

	return result, nil
}

func (a *StatusIOAdapter) parseIncident(inc StatusIOIncident) Incident {
	status := NormalizeIncidentStatus(inc.Status)
	var resolvedAt time.Time
	if status == StatusResolved {
		resolvedAt = parseTime(inc.UpdatedAt)
	}
	incident := Incident{
		ExtID:      inc.ID,
		Title:      inc.Name,
		Status:     status,
		Impact:     inc.Impact,
		StartedAt:  parseTime(inc.CreatedAt),
		ResolvedAt: resolvedAt,
		URL:        inc.Link,
	}

	// Aggregate body from updates if present
	if inc.Body != "" {
		incident.Body = inc.Body
	} else if len(inc.Updates) > 0 {
		var bodies []string
		for _, update := range inc.Updates {
			if update.Body != "" {
				bodies = append(bodies, fmt.Sprintf("[%s] %s", update.CreatedAt, update.Body))
			}
		}
		incident.Body = strings.Join(bodies, "\n\n")
	}

	return incident
}

func (a *StatusIOAdapter) parseScheduled(sched StatusIOScheduled) Incident {
	status := NormalizeIncidentStatus(sched.Status)
	endsAt := parseTime(sched.EndsAt)
	// A maintenance window whose end time has passed is finished.
	if !endsAt.IsZero() && endsAt.Before(time.Now().UTC()) {
		status = StatusResolved
	}
	var resolvedAt time.Time
	if status == StatusResolved {
		resolvedAt = endsAt
	}
	return Incident{
		ExtID:      sched.ID,
		Title:      sched.Name,
		Status:     status,
		StartedAt:  parseTime(sched.StartsAt),
		ResolvedAt: resolvedAt,
		URL:        sched.Link,
		Body:       fmt.Sprintf("Scheduled: starts %s, ends %s", sched.StartsAt, sched.EndsAt),
	}
}

func (a *StatusIOAdapter) normalizeComponentStatus(code int, status string) string {
	// status.io uses status codes:
	// 100-199: Operational/normal (green)
	// 200-299: Degraded (yellow)
	// 300-399: Partial outage (orange)
	// 400-499: Major outage (red)
	switch {
	case code >= 100 && code < 200:
		return "operational"
	case code >= 200 && code < 300:
		return "degraded_performance"
	case code >= 300 && code < 400:
		return "partial_outage"
	case code >= 400 && code < 500:
		return "major_outage"
	default:
		switch strings.ToLower(status) {
		case "operational":
			return "operational"
		case "degraded":
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
}

func (a *StatusIOAdapter) mapIndicator(indicator string) Indicator {
	switch indicator {
	case "Operational":
		return IndicatorNone
	case "Degraded":
		return IndicatorMinor
	case "Major":
		return IndicatorMajor
	case "Critical":
		return IndicatorCritical
	case "Maintenance":
		return IndicatorMaintenance
	case "Scheduled":
		return IndicatorMaintenance
	case "Incident":
		return IndicatorMajor
	default:
		// Fallback: check description for known keywords
		switch strings.ToLower(indicator) {
		case "none", "operational":
			return IndicatorNone
		case "minor", "degraded":
			return IndicatorMinor
		case "major", "partial_outage":
			return IndicatorMajor
		case "critical", "major_outage":
			return IndicatorCritical
		case "maintenance":
			return IndicatorMaintenance
		default:
			return IndicatorUnknown
		}
	}
}
