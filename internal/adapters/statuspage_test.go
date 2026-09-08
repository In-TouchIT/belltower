package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// summaryJSON is a StatusPage.io summary with one resolved and one live incident.
const summaryJSON = `{
  "page": {"id": "abc", "name": "Example"},
  "incidents": [
    {"id": "res1", "name": "Old outage", "status": "resolved", "impact": "major",
     "created_at": "2026-08-01T10:00:00Z", "updated_at": "2026-08-01T12:00:00Z",
     "shortlink": "https://stspg.io/res1"},
    {"id": "live1", "name": "Elevated errors", "status": "investigating", "impact": "minor",
     "created_at": "2026-09-01T10:00:00Z", "updated_at": "2026-09-01T10:30:00Z",
     "incident_updates": [{"body": "We are investigating.", "created_at": "2026-09-01T10:05:00Z"}]}
  ],
  "components": [
    {"id": "c1", "name": "API", "status": "operational", "updated_at": "2026-09-01T10:00:00Z"}
  ]
}`

func newTestAdapter(srv *httptest.Server) *StatusPageAdapter {
	return NewStatusPageAdapter(srv.Client(), "belltower-test")
}

func TestStatusPageParsesResolvedIncidents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(summaryJSON))
	}))
	defer srv.Close()

	res, err := newTestAdapter(srv).Fetch(context.Background(), ProviderInfo{ID: "x", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200", res.HTTPStatus)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}

	byID := map[string]Incident{}
	for _, inc := range res.Incidents {
		byID[inc.ExtID] = inc
	}

	// StatusPage.io says "resolved", not "closed". Keying off "closed" left
	// every finished incident looking open forever.
	resolved := byID["res1"]
	if resolved.Status != StatusResolved {
		t.Errorf("res1 status = %q, want %q", resolved.Status, StatusResolved)
	}
	if resolved.ResolvedAt.IsZero() {
		t.Error("res1 should carry a resolution timestamp")
	}
	if !resolved.Resolved() {
		t.Error("res1 should report Resolved() == true")
	}

	live := byID["live1"]
	if live.Status != StatusInvestigating {
		t.Errorf("live1 status = %q, want %q", live.Status, StatusInvestigating)
	}
	if !live.ResolvedAt.IsZero() {
		t.Errorf("live1 must not have a resolution timestamp, got %v", live.ResolvedAt)
	}
	// With no top-level body, updates are aggregated, each stamped with its time.
	if want := "[2026-09-01T10:05:00Z] We are investigating."; live.Body != want {
		t.Errorf("live1 body = %q, want %q", live.Body, want)
	}
	if want := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC); !live.StartedAt.Equal(want) {
		t.Errorf("live1 StartedAt = %v, want %v", live.StartedAt, want)
	}
}

func TestStatusPageIndicatorIgnoresResolvedIncidents(t *testing.T) {
	// The only major-impact incident is resolved, so the live minor one wins.
	a := &StatusPageAdapter{}
	got := a.computeIndicator(StatusPageSummary{
		Incidents: []StatusPageIncident{
			{Status: "resolved", Impact: "critical"},
			{Status: "investigating", Impact: "minor"},
		},
	})
	if got != IndicatorMinor {
		t.Errorf("computeIndicator = %q, want %q", got, IndicatorMinor)
	}
}

func TestStatusPageDegradedComponentIsNotOperational(t *testing.T) {
	a := &StatusPageAdapter{}
	got := a.computeIndicator(StatusPageSummary{
		Components: []StatusPageComponent{{Status: "degraded_performance"}},
	})
	if got != IndicatorMinor {
		t.Errorf("computeIndicator = %q, want %q", got, IndicatorMinor)
	}
}

// A failed API fetch must surface as an error so the poller records "unknown".
// The previous HTML fallback answered "operational" for any page containing
// <html> - including Cloudflare interstitials and outage banners.
func TestStatusPageDoesNotFabricateOperational(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"cloudflare block", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<!DOCTYPE html><html><body>Attention Required! Cloudflare</body></html>"))
		}},
		{"html instead of json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body>All Systems Down</body></html>"))
		}},
		{"not found", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusNotFound)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			res, err := newTestAdapter(srv).Fetch(context.Background(),
				ProviderInfo{ID: "x", Endpoint: srv.URL, PageURL: srv.URL})
			if err == nil {
				t.Fatalf("expected an error, got indicator %q", res.Indicator)
			}
			if res.Indicator == IndicatorNone {
				t.Error("a failed fetch must never report IndicatorNone")
			}
		})
	}
}

func TestStatusPageDoesNotRetryBotProtection(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newTestAdapter(srv).Fetch(context.Background(), ProviderInfo{ID: "x", Endpoint: srv.URL})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("made %d attempts, want 1: a 403 will fail identically on retry", attempts)
	}
}

func TestStatusPageHandlesGzip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http sets Accept-Encoding itself and decompresses transparently;
		// this asserts we did not break that by setting the header ourselves.
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("Accept-Encoding = %q, want the transport default %q", got, "gzip")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(summaryJSON))
	}))
	defer srv.Close()

	if _, err := newTestAdapter(srv).Fetch(context.Background(), ProviderInfo{ID: "x", Endpoint: srv.URL}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}
