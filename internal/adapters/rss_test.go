package adapters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func rssFeed(items string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Status</title>` + items + `</channel></rss>`
}

func rssItem(title, guid, desc string, when time.Time) string {
	return fmt.Sprintf(`<item><title>%s</title><guid>%s</guid><link>https://example.com/%s</link>
		<description>%s</description><pubDate>%s</pubDate></item>`,
		title, guid, guid, desc, when.Format(time.RFC1123Z))
}

func serveRSS(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// IBM Cloud's feed is a general notifications feed: 571 release notes and 130
// announcements, zero incidents. All of them used to become open incidents.
func TestRSSSkipsAnnouncementsAndReleaseNotes(t *testing.T) {
	now := time.Now().UTC()
	feed := rssFeed(
		rssItem("CLI version 10762 is available", "g1", "For more information. Type: release_note Regions: global", now.Add(-time.Hour)) +
			rssItem("Action Required: Prepare Your Automation", "g2", "What are we changing? Type: announcement", now.Add(-time.Hour)) +
			rssItem("Kubernetes version 131 is unsupported", "g3", "Update your cluster.", now.Add(-time.Hour)) +
			rssItem("Networking degradation in us-south", "g4", "We are investigating elevated errors.", now.Add(-time.Hour)),
	)

	srv := serveRSS(t, feed)
	res, err := NewRSSAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(res.Incidents) != 1 {
		ids := make([]string, len(res.Incidents))
		for i, inc := range res.Incidents {
			ids[i] = inc.Title
		}
		t.Fatalf("got %d incidents %v, want only the real one", len(res.Incidents), ids)
	}
	if res.Incidents[0].ExtID != "g4" {
		t.Errorf("kept %q, want the networking incident", res.Incidents[0].ExtID)
	}
}

// Feeds publish months of archive with no resolution marker, so age is the
// only signal that an entry is over.
func TestRSSAgesOutHistory(t *testing.T) {
	now := time.Now().UTC()
	feed := rssFeed(
		rssItem("Old outage in eu-west", "old1", "We are investigating errors.", now.Add(-90*24*time.Hour)) +
			rssItem("Fresh outage in us-east", "new1", "We are investigating errors.", now.Add(-2*time.Hour)),
	)

	srv := serveRSS(t, feed)
	res, err := NewRSSAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2 (both kept, only one active)", len(res.Incidents))
	}

	byID := map[string]Incident{}
	for _, inc := range res.Incidents {
		byID[inc.ExtID] = inc
	}
	if !byID["old1"].Resolved() {
		t.Errorf("a 90-day-old feed entry must not stay open: %+v", byID["old1"])
	}
	if byID["new1"].Resolved() {
		t.Errorf("a 2-hour-old entry should still be active: %+v", byID["new1"])
	}
}

func TestRSSResolvedItemIsNotActive(t *testing.T) {
	now := time.Now().UTC()
	srv := serveRSS(t, rssFeed(rssItem("Outage", "r1", "This incident has been resolved.", now.Add(-time.Hour))))

	res, err := NewRSSAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Indicator != IndicatorNone {
		t.Errorf("Indicator = %q, want none for a resolved incident", res.Indicator)
	}
	if len(res.Incidents) != 1 || res.Incidents[0].Status != StatusResolved {
		t.Errorf("incidents = %+v, want one resolved", res.Incidents)
	}
}

// A feed we cannot parse must be an error, not a silent "operational".
func TestRSSUnparseableFeedErrors(t *testing.T) {
	srv := serveRSS(t, "this is not xml at all")
	res, err := NewRSSAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err == nil {
		t.Fatalf("expected an error, got indicator %q", res.Indicator)
	}
	if res.Indicator == IndicatorNone {
		t.Error("a failed parse must not report IndicatorNone")
	}
}
