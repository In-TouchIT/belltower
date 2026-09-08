package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AppleAdapter handles Apple's status data feeds, which are JSON files
// (despite the .js extension). Two endpoints:
//
//   - Services:   https://www.apple.com/support/systemstatus/data/system_status_en_US.js
//   - Developer:  https://www.apple.com/support/systemstatus/data/developer/system_status_en_US.js (JSONP-wrapped)
//
// Each response has a top-level "services" array. Each service has an "events"
// array. An event with no eventStatus of "resolved" or "completed" is currently
// active; its statusType ("Outage" or "Performance Issue") indicates severity.
type AppleAdapter struct {
	client *http.Client
	ua     string
}

// AppleService represents one service in the feed.
type AppleService struct {
	ServiceNames string        `json:"serviceName"` // Note: some Apple responses use "serviceName"
	Events       []AppleEvent  `json:"events"`
}

// AppleEvent represents a status event for a service.
type AppleEvent struct {
	Message       string `json:"message"`
	StatusType    string `json:"statusType"`     // "Outage" or "Performance Issue"
	EventStatus   string `json:"eventStatus"`    // "resolved", "completed", or empty
	StartDate     string `json:"startDate"`      // e.g. "09/01/2026 03:15 PDT"
	EndDate       string `json:"endDate"`
	DatePosted    string `json:"datePosted"`
	UsersAffected string `json:"usersAffected"`
}

// appleStatusDoc models the JSON response.
type appleStatusDoc struct {
	Services []AppleService `json:"services"`
}

// NewAppleAdapter creates an Apple status adapter.
func NewAppleAdapter(client *http.Client, ua string) *AppleAdapter {
	return &AppleAdapter{client: client, ua: ua}
}

// Fetch implements the Adapter interface for Apple status pages.
func (a *AppleAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for Apple provider %s", p.Name)
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json, text/javascript")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to fetch Apple status: %w", err)
	}
	data := httpResp.Body

	// Apple's developer endpoint wraps the JSON in jsonCallback(...) — strip it.
	jsonStr := stripJSONP(data)

	var doc appleStatusDoc
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse Apple status JSON: %w", err)
	}

	result := Result{
		Indicator:  IndicatorNone,
		HTTPStatus: httpResp.StatusCode,
	}

	for _, svc := range doc.Services {
		incident := a.parseAppleService(svc)
		if incident != nil {
			result.Incidents = append(result.Incidents, *incident)
			// Map severity: Outage -> major, Performance Issue -> minor
			if strings.EqualFold(incident.Impact, "major") {
				if result.Indicator == IndicatorNone {
					result.Indicator = IndicatorMajor
				}
			} else if strings.EqualFold(incident.Impact, "minor") {
				if result.Indicator == IndicatorNone || result.Indicator == IndicatorMajor {
					result.Indicator = IndicatorMinor
				}
			}
		}
	}

	return result, nil
}

// parseAppleService converts an Apple service with active events into an
// Incident. Returns nil if all events are resolved/completed or there are none.
func (a *AppleAdapter) parseAppleService(svc AppleService) *Incident {
	var active *AppleEvent
	for i := range svc.Events {
		ev := &svc.Events[i]
		// Ignore resolved or completed events.
		if strings.EqualFold(ev.EventStatus, "resolved") ||
			strings.EqualFold(ev.EventStatus, "completed") {
			continue
		}
		active = ev
		break
	}
	if active == nil {
		return nil
	}

	impact := "minor"
	if strings.EqualFold(active.StatusType, "Outage") {
		impact = "major"
	}

	return &Incident{
		ExtID:     fmt.Sprintf("%s-%s", svc.ServiceNames, active.DatePosted),
		Title:     fmt.Sprintf("%s: %s", svc.ServiceNames, active.StatusType),
		Impact:    impact,
		Status:    "investigating",
		Body:      active.Message,
		StartedAt: parseAppleTime(active.StartDate),
		URL:       "https://www.apple.com/support/systemstatus/",
	}
}

// parseAppleTime parses Apple's date format like "09/01/2026 03:15 PDT".
func parseAppleTime(s string) time.Time {
	// Try common formats
	formats := []string{
		"01/02/2006 15:04 MST",
		"01/02/2006 15:04",
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

// stripJSONP removes a jsonCallback(...) wrapper if present.
func stripJSONP(data []byte) string {
	s := strings.TrimSpace(string(data))
	// Apple's developer feed wraps as: jsonCallback({...});
	// Strip the wrapper: find the first '(' and the last ')', then extract
	// everything between.
	if strings.HasPrefix(s, "jsonCallback") {
		start := strings.Index(s, "(")
		end := strings.LastIndex(s, ")")
		if start >= 0 && end > start {
			s = s[start+1 : end]
		}
	}
	return s
}
