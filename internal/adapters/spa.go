package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// SPAAdapter handles status pages that are Single Page Applications (SPAs)
// built with frameworks like React, Vue, Next.js, Nuxt.js.
//
// These pages load their data dynamically via JavaScript, so a simple HTTP
// request returns the HTML shell without the actual status data. This adapter
// uses either:
// 1. A headless browser (if CHROME_BIN or similar is available)
// 2. Extraction of embedded JSON data (__NEXT_DATA__, __NUXT_DATA__, etc.)
// 3. Server-side detection of JSON API endpoints
//
// The adapter is configured per-provider with an "extraction" pattern that
// tells it where to look for status data in the rendered page.
type SPAAdapter struct {
	client *http.Client
	ua     string
	// browserPath is the path to a headless browser binary
	browserPath string
}

// SPAConfig holds configuration for extracting status from an SPA
type SPAConfig struct {
	// ExtractionMethod can be: "nextdata", "nuxt", "embedded-json", "selector"
	ExtractionMethod string `yaml:"extraction_method"`
	// JSONPath is used with "embedded-json" method to locate status data
	JSONPath string `yaml:"json_path"`
	// ComponentsSelector is used with "selector" method
	ComponentsSelector string `yaml:"components_selector"`
	// StatusSelector indicates current overall status
	StatusSelector string `yaml:"status_selector"`
}

// NewSPAAdapter creates an SPA adapter. If browserPath is non-empty, it will
// attempt to use headless Chrome/Firefox for rendering.
func NewSPAAdapter(client *http.Client, ua string, browserPath string) *SPAAdapter {
	return &SPAAdapter{
		client:      client,
		ua:          ua,
		browserPath: browserPath,
	}
}

// Fetch implements the Adapter interface for SPA status pages.
func (a *SPAAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for SPA provider %s", p.Name)
	}

	// First, try rendering with a headless browser if available
	if a.browserPath != "" {
		return a.fetchWithBrowser(ctx, p, endpoint)
	}

	// Fall back to embedded JSON extraction
	return a.fetchWithExtraction(ctx, p, endpoint)
}

// fetchWithBrowser uses headless Chrome to render the page and extract status
func (a *SPAAdapter) fetchWithBrowser(ctx context.Context, p ProviderInfo, url string) (Result, error) {
	// Try using chromium/chrome in headless mode
	browser := a.browserPath
	if browser == "" {
		// Try common paths
		for _, path := range []string{"chromium-browser", "chromium", "google-chrome", "chrome"} {
			if _, err := exec.LookPath(path); err == nil {
				browser = path
				break
			}
		}
	}

	if browser == "" {
		return a.fetchWithExtraction(ctx, p, url)
	}

	// Render the page and extract content
	cmd := exec.CommandContext(ctx, browser,
		"--headless",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--dump-dom",
		"--timeout=10000",
		url,
	)

	output, err := cmd.Output()
	if err != nil {
		// Browser failed, fall back to extraction
		return a.fetchWithExtraction(ctx, p, url)
	}

	html := string(output)
	return a.extractFromHTML(ctx, p, html)
}

// fetchWithExtraction fetches the HTML directly and tries to extract
// embedded JSON data without rendering JavaScript
func (a *SPAAdapter) fetchWithExtraction(ctx context.Context, p ProviderInfo, url string) (Result, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, url, "text/html")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to fetch SPA page: %w", err)
	}

	html := string(httpResp.Body)
	return a.extractFromHTML(ctx, p, html)
}

// extractFromHTML tries multiple extraction methods to find status data
func (a *SPAAdapter) extractFromHTML(ctx context.Context, p ProviderInfo, html string) (Result, error) {
	result := Result{
		Indicator:  IndicatorUnknown,
		HTTPStatus: 200,
	}

	// Method 1: Extract __NEXT_DATA__
	if data, ok := extractNextData(html); ok {
		// Try to find status information in Next.js data
		if status := findStatusInJSON(data); status != "" {
			result.Indicator = mapStringIndicator(status)
		}
		result.Components = extractComponentsFromNextData(data)
		return result, nil
	}

	// Method 2: Extract __NUXT_DATA__
	if data, ok := extractNuxtData(html); ok {
		if status := findStatusInJSON(data); status != "" {
			result.Indicator = mapStringIndicator(status)
		}
		return result, nil
	}

	// Method 3: Look for embedded JSON with known patterns
	if data, ok := extractEmbeddedJSON(html, "window.__INITIAL_STATE__"); ok {
		if status := findStatusInJSON(data); status != "" {
			result.Indicator = mapStringIndicator(status)
		}
		return result, nil
	}

	// Method 4: Try to find API endpoints in the HTML and call them
	if apiURL, ok := findAPIEndpoints(html); ok {
		apiResult, apiErr := a.fetchAPI(ctx, apiURL)
		if apiErr == nil {
			return apiResult, nil
		}
	}

	// If we couldn't extract any data, we need to report this as a failure
	// rather than assuming operational - we don't know the status
	return result, fmt.Errorf("could not extract status data from SPA page %s", p.Name)
}

// fetchAPI attempts to fetch a discovered API endpoint
func (a *SPAAdapter) fetchAPI(ctx context.Context, apiURL string) (Result, error) {
	httpResp, err := httpGet(ctx, a.client, a.ua, apiURL, "application/json")
	if err != nil {
		return Result{}, fmt.Errorf("failed to fetch API endpoint: %w", err)
	}

	// Try to parse as StatusPage format
	var statuspage map[string]interface{}
	if err := json.Unmarshal(httpResp.Body, &statuspage); err == nil {
		if _, ok := statuspage["page"]; ok {
			return Result{
				Indicator:  IndicatorNone,
				HTTPStatus: httpResp.StatusCode,
			}, nil
		}
	}

	return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("could not parse API response")
}

// extractNextData finds and extracts __NEXT_DATA__ JSON from HTML
func extractNextData(html string) (string, bool) {
	idx := strings.Index(html, "__NEXT_DATA__")
	if idx < 0 {
		return "", false
	}
	// Find the JSON block
	start := strings.Index(html[idx:], ">")
	if start < 0 {
		return "", false
	}
	start += idx + 1
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return "", false
	}
	return html[start : start+end], true
}

// extractNuxtData finds and extracts __NUXT_DATA__ JSON from HTML
func extractNuxtData(html string) (string, bool) {
	idx := strings.Index(html, "__NUXT_DATA__")
	if idx < 0 {
		return "", false
	}
	start := strings.Index(html[idx:], "type=\"application/json\">")
	if start < 0 {
		return "", false
	}
	start += idx + len("type=\"application/json\">")
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return "", false
	}
	return html[start : start+end], true
}

// extractEmbeddedJSON finds JSON from a named global variable
func extractEmbeddedJSON(html, varName string) (string, bool) {
	pattern := varName + " = "
	idx := strings.Index(html, pattern)
	if idx < 0 {
		pattern = varName + "= "
		idx = strings.Index(html, pattern)
	}
	if idx < 0 {
		return "", false
	}
	start := idx + len(pattern)
	// Find the end of the JSON (next semicolon on its own or end of script)
	braceCount := 0
	inString := false
	end := start
	for i := start; i < len(html); i++ {
		ch := html[i]
		if ch == '"' && (i == 0 || html[i-1] != '\\') {
			inString = !inString
		}
		if inString {
			continue
		}
		if ch == '{' {
			braceCount++
		} else if ch == '}' {
			braceCount--
			if braceCount == 0 {
				end = i + 1
				break
			}
		}
	}
	if end > start {
		return html[start:end], true
	}
	return "", false
}

// findAPIEndpoints looks for API URLs in the HTML
func findAPIEndpoints(html string) (string, bool) {
	// Look for patterns like /api/v2/summary.json
	patterns := []string{
		"/api/v2/summary.json",
		"/api/v1/status",
		"/api/status",
		"/api/v2/incidents.json",
	}
	
	for _, pattern := range patterns {
		idx := strings.Index(html, pattern)
		if idx >= 0 {
			// Extract the full URL if possible
			start := strings.LastIndex(html[:idx], "\"")
			if start < 0 {
				start = idx
			}
			end := strings.Index(html[idx:], "\"")
			if end > 0 {
				return html[start : idx+end], true
			}
			return pattern, true
		}
	}
	return "", false
}

// findStatusInJSON looks for status indicators in parsed JSON data
func findStatusInJSON(data string) string {
	// Look for common status patterns in the JSON
	statusIndicators := []string{
		`"indicator":"none"`,
		`"indicator":"minor"`,
		`"indicator":"major"`,
		`"indicator":"critical"`,
		`"state":"operational"`,
		`"state":"degraded"`,
		`"state":"outage"`,
		`"health":"healthy"`,
		`"health":"unhealthy"`,
		`"status":"operational"`,
		`"status":"degraded"`,
		`"status":"major"`,
	}
	
	for _, pattern := range statusIndicators {
		if strings.Contains(data, pattern) {
			return strings.ToLower(strings.Split(pattern, ":")[1][1 : len(strings.Split(pattern, ":")[1])-1])
		}
	}
	return ""
}

// extractComponentsFromNextData tries to extract component info from Next.js data
func extractComponentsFromNextData(data string) []Component {
	// This is a simplified extraction - a full implementation would parse
	// the Next.js data structure recursively
	return nil
}

// mapStringIndicator maps string status to our Indicator type
func mapStringIndicator(status string) Indicator {
	switch status {
	case "none", "operational", "healthy", "success":
		return IndicatorNone
	case "minor", "degraded", "performance":
		return IndicatorMinor
	case "major", "partial_outage":
		return IndicatorMajor
	case "critical", "full_outage":
		return IndicatorCritical
	case "maintenance", "under_maintenance":
		return IndicatorMaintenance
	default:
		return IndicatorUnknown
	}
}

// Check if headless browser is available on the system
func detectBrowserPath() string {
	for _, path := range []string{
		"chromium-browser",
		"chromium",
		"google-chrome",
		"google-chrome-stable",
		"chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/app/.local/bin/chromium",
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		if loc, err := exec.LookPath(path); err == nil {
			return loc
		}
	}
	return ""
}
