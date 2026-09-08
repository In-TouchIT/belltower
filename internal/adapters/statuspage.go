package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
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
		ID       string `json:"id"`
		Name     string `json:"name"`
		URL      string `json:"url"`
		TimeZone string `json:"time_zone"`
	} `json:"page"`
	ActiveIncidentCount int                   `json:"active_incident_count"`
	Incidents           []StatusPageIncident  `json:"incidents"`
	Components          []StatusPageComponent `json:"components"`
}

type StatusPageIncident struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Body      string             `json:"body"`
	Status    string             `json:"status"`
	Impact    string             `json:"impact"`
	URL       string             `json:"shortlink"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
	Updates   []StatusPageUpdate `json:"incident_updates"`
}

type StatusPageUpdate struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type StatusPageComponent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
		base := strings.TrimSuffix(p.PageURL, "/")
		if base == "" {
			return Result{}, fmt.Errorf("no endpoint or page URL for %s", p.ID)
		}
		endpoint = base + "/api/v2/summary.json"
	}

	// A failed fetch is reported as a failure. There is deliberately no
	// "the HTML page loaded, so assume operational" fallback here: a
	// Cloudflare challenge, a login wall, and a page announcing a total
	// outage are all indistinguishable HTML, and reporting any of them as
	// operational is the one answer a status monitor must never give.
	return a.fetchWithRetry(ctx, endpoint, 3)
}

// fetchWithRetry attempts to fetch the status page with exponential backoff
func (a *StatusPageAdapter) fetchWithRetry(ctx context.Context, endpoint string, maxRetries int) (Result, error) {
	var lastErr error
	var lastResult Result
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(backoff):
			}
		}

		result, err := a.fetchOnce(ctx, endpoint)
		if err == nil {
			return result, nil
		}

		lastErr = err
		lastResult = result
		if !isRetryableError(err) {
			return lastResult, err
		}

		if attempt < maxRetries {
			log.Printf("Retry %d/%d for %s after error: %v", attempt+1, maxRetries, endpoint, err)
		}
	}

	return lastResult, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// isRetryableError reports whether an error is worth another attempt.
//
// Rate limiting and transient network faults are retryable. Bot protection
// (403), auth walls (401) and missing endpoints (404) are not: the same request
// will fail identically, and retrying three times per cycle multiplies the load
// we put on a provider that has already told us to go away.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	for _, terminal := range []string{"status 401", "status 403", "status 404", "access denied", "not found"} {
		if strings.Contains(errStr, terminal) {
			return false
		}
	}
	for _, retryable := range []string{"429", "rate limited", "timeout", "connection reset", "temporary failure", "eof", "no such host"} {
		if strings.Contains(errStr, retryable) {
			return true
		}
	}
	return false
}

// fetchOnce performs a single fetch attempt
func (a *StatusPageAdapter) fetchOnce(ctx context.Context, endpoint string) (Result, error) {
	resp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/json")
	if err != nil {
		return Result{HTTPStatus: resp.StatusCode}, err
	}

	// An HTML body means this is not a StatusPage.io API endpoint - most
	// often a redirect to a marketing page or a bot-protection interstitial.
	if looksLikeHTML(resp.ContentType, resp.Body) {
		return Result{HTTPStatus: resp.StatusCode},
			fmt.Errorf("HTML response instead of JSON API - provider may not use StatusPage.io")
	}

	var summary StatusPageSummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		return Result{HTTPStatus: resp.StatusCode}, fmt.Errorf("failed to decode response: %w", err)
	}

	result := Result{HTTPStatus: resp.StatusCode}
	result.Indicator = a.computeIndicator(summary)

	for _, inc := range summary.Incidents {
		result.Incidents = append(result.Incidents, a.parseIncident(inc))
	}

	for _, comp := range summary.Components {
		result.Components = append(result.Components, Component{
			Name:      comp.Name,
			Status:    a.normalizeStatus(comp.Status),
			UpdatedAt: parseTime(comp.UpdatedAt),
		})
	}

	return result, nil
}

// computeIndicator determines the overall indicator from components and incidents
func (a *StatusPageAdapter) computeIndicator(summary StatusPageSummary) Indicator {
	hasMinor := false
	hasMajor := false
	hasMaintenance := false

	for _, inc := range summary.Incidents {
		if IsResolvedStatus(inc.Status) {
			continue
		}
		switch inc.Impact {
		case "critical":
			return IndicatorCritical
		case "major":
			hasMajor = true
		case "minor":
			hasMinor = true
		case "maintenance":
			hasMaintenance = true
		}
	}

	for _, comp := range summary.Components {
		switch a.normalizeStatus(comp.Status) {
		case "major_outage":
			return IndicatorCritical
		case "partial_outage":
			hasMajor = true
		case "degraded_performance":
			hasMinor = true
		case "maintenance":
			hasMaintenance = true
		}
	}

	switch {
	case hasMajor:
		return IndicatorMajor
	case hasMinor:
		return IndicatorMinor
	case hasMaintenance:
		return IndicatorMaintenance
	default:
		return IndicatorNone
	}
}

// parseIncident converts a StatusPage incident to our Incident type
func (a *StatusPageAdapter) parseIncident(inc StatusPageIncident) Incident {
	status := NormalizeIncidentStatus(inc.Status)

	// StatusPage.io marks a finished incident "resolved" (or "postmortem"),
	// never "closed", and reports no explicit resolution timestamp - the last
	// update is the resolution, so updated_at is the best available answer.
	var resolvedAt time.Time
	if status == StatusResolved {
		resolvedAt = parseTime(inc.UpdatedAt)
	}

	// Use the incident body, but if it's empty, aggregate updates
	body := inc.Body
	if body == "" && len(inc.Updates) > 0 {
		var updateBodies []string
		for i := len(inc.Updates) - 1; i >= 0; i-- {
			if inc.Updates[i].Body != "" {
				updateBodies = append(updateBodies, fmt.Sprintf("[%s] %s", inc.Updates[i].CreatedAt, inc.Updates[i].Body))
			}
		}
		body = strings.Join(updateBodies, "\n\n")
	}

	return Incident{
		ExtID:      inc.ID,
		Title:      inc.Name,
		Body:       body,
		Status:     status,
		Impact:     inc.Impact,
		StartedAt:  parseTime(inc.CreatedAt),
		ResolvedAt: resolvedAt,
		URL:        inc.URL,
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
