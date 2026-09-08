package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Field names here are GCP's real ones. An earlier adapter looked for
// title/content/start_time/status, none of which exist, so every GCP incident
// was stored with an empty title and body.
const gcpIncidentsJSON = `[
  {"id": "past1", "begin": "2026-09-01T14:44:00+00:00", "end": "2026-09-01T18:52:00+00:00",
   "external_desc": "Network degradation in us-central1-b", "severity": "medium",
   "status_impact": "SERVICE_DISRUPTION", "uri": "incidents/past1",
   "most_recent_update": {"text": "Postmortem published."},
   "affected_products": [{"title": "Compute Engine", "id": "ce"}]},
  {"id": "live1", "begin": "2026-09-03T09:00:00+00:00", "end": "",
   "external_desc": "Elevated BigQuery errors", "severity": "high",
   "status_impact": "SERVICE_OUTAGE", "uri": "incidents/live1",
   "most_recent_update": {"text": "We are investigating."},
   "affected_products": [{"title": "BigQuery", "id": "bq"}]}
]`

func TestGCPUsesRealFieldNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(gcpIncidentsJSON))
	}))
	defer srv.Close()

	res, err := NewGCPAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}
	for _, inc := range res.Incidents {
		if inc.Title == "" {
			t.Errorf("incident %s has an empty title", inc.ExtID)
		}
		if inc.Body == "" {
			t.Errorf("incident %s has an empty body", inc.ExtID)
		}
		if inc.StartedAt.IsZero() {
			t.Errorf("incident %s has no start time", inc.ExtID)
		}
	}

	byID := map[string]Incident{}
	for _, inc := range res.Incidents {
		byID[inc.ExtID] = inc
	}
	if byID["past1"].Status != StatusResolved {
		t.Errorf("past1 status = %q, want resolved (it has an end time)", byID["past1"].Status)
	}
	if byID["live1"].Status != StatusOpen {
		t.Errorf("live1 status = %q, want open", byID["live1"].Status)
	}

	// The live SERVICE_OUTAGE must drive the indicator, not the resolved one.
	if res.Indicator != IndicatorCritical {
		t.Errorf("Indicator = %q, want %q", res.Indicator, IndicatorCritical)
	}

	// Only live incidents contribute components.
	if len(res.Components) != 1 || res.Components[0].Name != "BigQuery" {
		t.Errorf("components = %+v, want only the live incident's products", res.Components)
	}
}
