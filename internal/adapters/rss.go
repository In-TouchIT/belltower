package adapters

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RSSAdapter handles RSS-based status feeds
// Examples:
// - Azure: https://azurestatuscdn.azureedge.net/en-us/status/feed/
// - OCI: https://ocistatus.oraclecloud.com/api/v2/incident-summary.rss
// - Hetzner: https://status.hetzner.com/en.atom
type RSSAdapter struct {
	client *http.Client
	ua     string
}

// RSS is the standard RSS 2.0 structure
type RSS struct {
	XMLName xml.Name   `xml:"rss"`
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

// AtomFeed is the Atom Syndication Format
type AtomFeed struct {
	XMLName xml.Name   `xml:"feed"`
	Title   string     `xml:"title"`
	Link    AtomLink   `xml:"link"`
	Updated string     `xml:"updated"`
	Entries []AtomEntry `xml:"entry"`
}

type AtomLink struct {
	Href string `xml:"href,attr"`
}

type AtomEntry struct {
	Title     string     `xml:"title"`
	Link      AtomLink   `xml:"link"`
	ID        string     `xml:"id"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	Content   AtomContent `xml:"content"`
	Summary   string     `xml:"summary"`
}

type AtomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// ParseFeed attempts to parse XML as either RSS 2.0 or Atom format.
// Returns normalized items regardless of source format.
func parseFeed(data []byte) ([]FeedItem, error) {
	// Try RSS first
	if strings.Contains(string(data), "<rss") {
		var rss RSS
		if err := xml.Unmarshal(data, &rss); err == nil {
			items := make([]FeedItem, 0, len(rss.Channel.Items))
			for _, item := range rss.Channel.Items {
				items = append(items, FeedItem{
					Title:       item.Title,
					Link:        item.Link,
					Description: item.Description,
					PubDate:     item.PubDate,
					GUID:        item.GUID,
				})
			}
			return items, nil
		}
	}
	
	// Try Atom
	if strings.Contains(string(data), "<feed") {
		var atom AtomFeed
		if err := xml.Unmarshal(data, &atom); err == nil {
			items := make([]FeedItem, 0, len(atom.Entries))
			for _, entry := range atom.Entries {
				content := entry.Content.Text
				if content == "" {
					content = entry.Summary
				}
				items = append(items, FeedItem{
					Title:       entry.Title,
					Link:        entry.Link.Href,
					Description: content,
					PubDate:     entry.Updated,
					GUID:        entry.ID,
				})
			}
			return items, nil
		}
	}
	
	return nil, fmt.Errorf("unable to parse XML feed")
}

// FeedItem is a normalized item from either RSS or Atom
type FeedItem struct {
	Title       string
	Link        string
	Description string
	PubDate     string
	GUID        string
}

func NewRSSAdapter(client *http.Client, ua string) *RSSAdapter {
	return &RSSAdapter{client: client, ua: ua}
}

func (a *RSSAdapter) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		return Result{}, fmt.Errorf("no endpoint for RSS provider %s", p.Name)
	}

	httpResp, err := httpGet(ctx, a.client, a.ua, endpoint, "application/rss+xml, application/xml, text/xml")
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, err
	}
	data := httpResp.Body

	result := Result{
		Indicator:  IndicatorNone,
		HTTPStatus: httpResp.StatusCode,
	}

	// A feed we cannot parse is reported as an error, not as "operational".
	items, err := parseFeed(data)
	if err != nil {
		return Result{HTTPStatus: httpResp.StatusCode}, fmt.Errorf("failed to parse RSS/Atom feed: %w", err)
	}

	// A status RSS feed is an archive, not a list of live incidents, and some
	// (IBM Cloud's, for one) are general notification feeds carrying mostly
	// release notes and announcements. Both have to be filtered out, or every
	// historical entry is reported as a currently-open incident.
	now := time.Now().UTC()
	for _, item := range items {
		incident := a.parseFeedItem(item)
		if incident.Title == "" {
			continue
		}
		if isInformationalItem(incident.Title, incident.Body) {
			continue
		}

		// An entry older than the active window is history: RSS carries no
		// resolution marker, so age is the only signal that it is over.
		stale := !incident.StartedAt.IsZero() && now.Sub(incident.StartedAt) > rssActiveWindow
		if stale {
			incident.Status = StatusResolved
			if incident.ResolvedAt.IsZero() {
				incident.ResolvedAt = incident.StartedAt
			}
		}

		result.Incidents = append(result.Incidents, incident)

		if !stale && a.isIncidentActive(incident) {
			if a.isIncidentCritical(incident) {
				result.Indicator = IndicatorMajor
			} else if result.Indicator == IndicatorNone {
				result.Indicator = IndicatorMinor
			}
		}
	}

	return result, nil
}

// rssActiveWindow is how recent a feed entry must be to count as a live
// incident. Feeds publish months of history with no resolution marker.
const rssActiveWindow = 7 * 24 * time.Hour

// informationalMarkers identify feed entries that are not incidents at all.
// Several vendors publish release notes, deprecations and announcements on the
// same feed as incidents, and many feeds tag the entry type explicitly.
var informationalMarkers = []string{
	"type: release_note",
	"type: announcement",
	"type: release note",
	"type:release_note",
	"type:announcement",
}

// informationalTitles catch untagged notices by their conventional wording.
var informationalTitles = []string{
	"is unsupported",
	"is available",
	"release note",
	"end of support",
	"end of life",
	"deprecat",
	"action required:",
	"is now generally available",
	"retirement",
	"will be retired",
}

// isInformationalItem reports whether a feed entry is an announcement or
// release note rather than a service incident.
func isInformationalItem(title, body string) bool {
	haystack := strings.ToLower(body)
	for _, marker := range informationalMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	lowerTitle := strings.ToLower(title)
	for _, marker := range informationalTitles {
		if strings.Contains(lowerTitle, marker) {
			return true
		}
	}
	return false
}

// parseFeedItem converts a normalized FeedItem (from RSS or Atom) to our Incident type
func (a *RSSAdapter) parseFeedItem(item FeedItem) Incident {
	title := strings.TrimSpace(item.Title)
	description := a.stripHTML(item.Description)

	return Incident{
		ExtID:     strings.TrimSpace(item.GUID),
		Title:     title,
		Body:      description,
		Status:    NormalizeIncidentStatus(a.extractStatusFromDescription(description)),
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
	return !IsResolvedStatus(inc.Status)
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
