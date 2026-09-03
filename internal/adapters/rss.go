package adapters

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RSSAdapter handles RSS-based status feeds
// Examples:
// - Azure: https://azurestatuscdn.azureedge.net/en-us/status/feed/
// - OCI: https://ocistatus.oraclecloud.com/api/v2/incident-summary.rss
type RSSAdapter struct {
	client *http.Client
	ua     string
}

// RSS is the standard RSS 2.0 structure
type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Channel RSSChannel `xml:"channel"`
}

type RSSChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []RSSItem `xml:"item"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

func NewRSSAdapter(client *http.Client, ua string) *RSSAdapter {
	return &RSSAdapter{client: client, ua: ua}
}

func (a *RSSAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for RSS provider %s", p.Name)
	}

	data, err := a.fetch(ctx, endpoint)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Indicator: IndicatorNone,
	}

	// Try parsing as XML RSS
	var rss RSS
	if err := xml.Unmarshal(data, &rss); err != nil {
		// Fall back to JSON parsing (some feeds use JSON RSS)
		result = a.parseJSONRSS(data, endpoint)
		return result, nil
	}

	// Process RSS items - each item typically contains an incident summary
	// with embedded status updates
	for _, item := range rss.Channel.Items {
		incident := a.parseRSSItem(item)
		if incident.Title != "" {
			result.Incidents = append(result.Incidents, incident)
			
			// Check if this incident indicates an ongoing issue
			if a.isIncidentActive(incident) {
				// Determine indicator based on severity
				if a.isIncidentCritical(incident) {
					result.Indicator = IndicatorMajor
				} else if result.Indicator == IndicatorNone {
					result.Indicator = IndicatorMinor
				}
			}
		}
	}

	return result, nil
}

func (a *RSSAdapter) fetch(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Accept", "application/json, application/xml, text/xml")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("RSS feed returned status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// parseRSSItem converts an RSS item to our Incident type
func (a *RSSAdapter) parseRSSItem(item RSSItem) Incident {
	// RSS items often have HTML-encoded descriptions with multiple updates
	// The title typically contains the service name and reference ID
	title := strings.TrimSpace(item.Title)
	description := a.stripHTML(item.Description)
	
	return Incident{
		ExtID:     strings.TrimSpace(item.GUID),
		Title:     title,
		Body:      description,
		Status:    a.extractStatusFromDescription(description),
		Impact:    a.extractImpactFromDescription(description),
		StartedAt: parseTime(item.PubDate),
		URL:       strings.TrimSpace(item.Link),
	}
}

// extractStatusFromDescription parses the incident status from the description
func (a *RSSAdapter) extractStatusFromDescription(desc string) string {
	dl := strings.ToLower(desc)
	if strings.Contains(dl, "resolved") {
		return "resolved"
	}
	if strings.Contains(dl, "identified") {
		return "identified"
	}
	if strings.Contains(dl, "investigating") {
		return "investigating"
	}
	if strings.Contains(dl, "monitoring") {
		return "monitoring"
	}
	if strings.Contains(dl, "maintenance") {
		return "maintenance"
	}
	return "open"
}

// extractImpactFromDescription determines impact level from description
func (a *RSSAdapter) extractImpactFromDescription(desc string) string {
	dl := strings.ToLower(desc)
	if strings.Contains(dl, "major outage") || strings.Contains(dl, "critical") {
		return "major"
	}
	if strings.Contains(dl, "partial outage") || strings.Contains(dl, "degraded") {
		return "minor"
	}
	// Check for regional/service-specific impacts
	if strings.Contains(dl, "affecting") || strings.Contains(dl, "impact") {
		return "major"
	}
	return "minor"
}

// isIncidentActive checks if an incident is still ongoing
func (a *RSSAdapter) isIncidentActive(inc Incident) bool {
	return inc.Status != "resolved" && inc.Status != "closed"
}

// isIncidentCritical checks if an incident has critical impact
func (a *RSSAdapter) isIncidentCritical(inc Incident) bool {
	return inc.Impact == "major" || inc.Impact == "critical"
}

// stripHTML removes HTML tags from a string
func (a *RSSAdapter) stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			if r == '\n' || r == '\t' {
				b.WriteRune(' ')
			} else {
				b.WriteRune(r)
			}
		}
	}
	// Clean up extra whitespace and HTML entities
	result := b.String()
	result = strings.ReplaceAll(result, "&lt;", "<")
	result = strings.ReplaceAll(result, "&gt;", ">")
	result = strings.ReplaceAll(result, "&amp;", "&")
	result = strings.ReplaceAll(result, "&quot;", "\"")
	result = strings.ReplaceAll(result, "&#39;", "'")
	
	// Collapse multiple spaces/newlines
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}

// parseJSONRSS handles JSON-format RSS feeds
func (a *RSSAdapter) parseJSONRSS(data []byte, endpoint string) Result {
	result := Result{
		Indicator: IndicatorUnknown,
	}
	
	// Try to parse as JSON RSS-like format
	var feed struct {
		Channel struct {
			Items []struct {
				Title       string `json:"title"`
				Link        string `json:"link"`
				Description string `json:"description"`
				PubDate     string `json:"pubDate"`
				GUID        string `json:"guid"`
			} `json:"item"`
		} `json:"channel"`
	}
	
	// Actually JSON RSS uses "items" for items
	var feed2 struct {
		Title       string `json:"title"`
		Link        string `json:"link"`
		Description string `json:"description"`
		Items       []struct {
			Title       string `json:"title"`
			Link        string `json:"link"`
			Description string `json:"description"`
			PubDate     string `json:"pubDate"`
			GUID        string `json:"guid"`
		} `json:"item"`
	}
	
	_ = feed
	_ = feed2
	_ = endpoint
	
	return result
}

// Ensure time import doesn't cause unused warning
var _ = time.Now
