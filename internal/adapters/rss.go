package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RSSAdapter handles RSS-based status feeds
// Example: Azure status https://azurestatuscdn.azureedge.net/en-us/status/feed/
type RSSAdapter struct {
	client *http.Client
	ua     string
}

type RSSFeed struct {
	Channel RSSChannel `json:"channel"`
}

type RSSChannel struct {
	Title  string    `json:"title"`
	Link   string    `json:"link"`
	Items  []RSSItem `json:"item"`
}

type RSSItem struct {
	Title     string `json:"title"`
	Link      string `json:"link"`
	Description string `json:"description"`
	GUID      string `json:"guid"`
	Published string `json:"pubDate"`
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

	// Parse RSS as JSON (Azure uses JSON RSS format)
	var feed RSSFeed
	if err := json.Unmarshal(data, &feed); err != nil {
		// Fall back to XML parsing
		result := a.parseRSSXML(data, endpoint)
		return result, nil
	}

	result := Result{
		Indicator: IndicatorNone,
	}

	// Check if any items mention "outage" or "degraded"
	for _, item := range feed.Channel.Items {
		title := strings.ToLower(item.Title)
		desc := strings.ToLower(item.Description)
		
		if strings.Contains(title, "outage") || strings.Contains(desc, "outage") ||
			strings.Contains(title, "degraded") || strings.Contains(desc, "degraded") ||
			strings.Contains(title, "incident") || strings.Contains(desc, "incident") {
			result.Indicator = IndicatorMajor
			
			result.Incidents = append(result.Incidents, Incident{
				ExtID:      item.GUID,
				Title:      item.Title,
				Body:       stripHTML(item.Description),
				Status:     "investigating",
				Impact:     "major",
				StartedAt:  parseTime(item.Published),
				URL:        item.Link,
				RawJSON:    string(data),
			})
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
	req.Header.Set("Accept", "application/json, application/xml")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (a *RSSAdapter) parseRSSXML(data []byte, endpoint string) Result {
	// Simple XML parsing fallback
	result := Result{Indicator: IndicatorUnknown}
	
	// This is a simplified XML parser - in production we'd use encoding/xml
	content := string(data)
	
	if strings.Contains(strings.ToLower(content), "error") || strings.Contains(strings.ToLower(content), "outage") {
		result.Indicator = IndicatorMajor
	}
	
	return result
}

func stripHTML(s string) string {
	// Simple HTML tag stripper
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	
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
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Ensure time import doesn't cause unused warning
var _ = time.Now
