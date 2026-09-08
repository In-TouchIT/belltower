package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Google Workspace publishes the same schema as Google Cloud. The adapter used
// to read title/body/impact/begin, of which only begin exists, so every
// Workspace incident was stored with an empty title.
const gworkspaceJSON = `[
  {"id": "w1", "begin": "2026-08-28T17:25:00+00:00", "end": "2026-08-28T20:45:00+00:00",
   "external_desc": "Google Chat users experiencing message delays", "severity": "low",
   "status_impact": "SERVICE_INFORMATION", "uri": "incidents/w1",
   "most_recent_update": {"text": "The issue is resolved."},
   "affected_products": [{"title": "Google Chat", "id": "chat"}]},
  {"id": "w2", "begin": "2026-09-03T08:00:00+00:00", "end": "",
   "external_desc": "Gmail attachment failures", "severity": "high",
   "status_impact": "SERVICE_DISRUPTION", "uri": "incidents/w2",
   "most_recent_update": {"text": "We are investigating."},
   "affected_products": [{"title": "Gmail", "id": "gmail"}]}
]`

func TestGWorkspaceParsesTitlesAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(gworkspaceJSON))
	}))
	defer srv.Close()

	res, err := NewGWorkspaceAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}

	byID := map[string]Incident{}
	for _, inc := range res.Incidents {
		if inc.Title == "" {
			t.Errorf("incident %s has an empty title", inc.ExtID)
		}
		byID[inc.ExtID] = inc
	}

	if byID["w1"].Status != StatusResolved {
		t.Errorf("w1 status = %q, want resolved (it has an end time)", byID["w1"].Status)
	}
	if byID["w2"].Status != StatusOpen {
		t.Errorf("w2 status = %q, want open", byID["w2"].Status)
	}
	if want := "Gmail attachment failures"; byID["w2"].Title != want {
		t.Errorf("w2 title = %q, want %q", byID["w2"].Title, want)
	}
	if res.Indicator != IndicatorMajor {
		t.Errorf("Indicator = %q, want major from the live SERVICE_DISRUPTION", res.Indicator)
	}
	if len(res.Components) != 1 || res.Components[0].Name != "Gmail" {
		t.Errorf("components = %+v, want only the live incident's products", res.Components)
	}
}

// Salesforce's Trust API has no name/details/url field at all.
const salesforceJSON = `[
  {"id": "20004309", "externalId": "93435306", "status": "Resolved", "type": "Degradation",
   "createdAt": "2026-07-29T08:51:38.364Z", "updatedAt": "2026-08-01T05:14:30.114Z",
   "serviceKeys": ["coreService"], "isCore": false,
   "message": {"rootCause": null, "actionPlan": null},
   "IncidentImpacts": [{"id": 1, "startTime": "2026-07-24T20:01:00.000Z", "endTime": "2026-08-01T03:30:00.000Z",
                        "type": "featurePerfDegradation", "severity": "minor"}],
   "IncidentEvents": [{"id": 1, "type": "update", "message": "Lightning report exports were degraded."}]},
  {"id": "20004999", "externalId": "93999999", "status": "Active", "type": "Disruption",
   "createdAt": "2026-09-03T08:00:00.000Z", "updatedAt": "2026-09-03T09:00:00.000Z",
   "serviceKeys": ["marketingCloud"],
   "IncidentImpacts": [{"id": 2, "startTime": "2026-09-03T08:00:00.000Z", "endTime": "",
                        "type": "serviceDisruption", "severity": "major"}],
   "IncidentEvents": [{"id": 2, "type": "update", "message": "We are investigating a disruption."}]}
]`

func TestSalesforceBuildsTitlesFromRealSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(salesforceJSON))
	}))
	defer srv.Close()

	res, err := NewSalesforceAdapter(srv.Client(), "t").Fetch(context.Background(), ProviderInfo{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(res.Incidents))
	}

	byID := map[string]Incident{}
	for _, inc := range res.Incidents {
		if inc.Title == "" {
			t.Errorf("incident %s has an empty title", inc.ExtID)
		}
		if inc.Body == "" {
			t.Errorf("incident %s has an empty body", inc.ExtID)
		}
		byID[inc.ExtID] = inc
	}

	resolved := byID["93435306"]
	if resolved.Status != StatusResolved {
		t.Errorf("status = %q, want resolved (API says \"Resolved\")", resolved.Status)
	}
	if resolved.ResolvedAt.IsZero() {
		t.Error("resolved incident should carry an end time from its impact")
	}
	if want := "Feature Perf Degradation - coreService"; resolved.Title != want {
		t.Errorf("title = %q, want %q", resolved.Title, want)
	}
	// startedAt comes from the impact, which predates createdAt.
	if got := resolved.StartedAt.Format("2006-01-02"); got != "2026-07-24" {
		t.Errorf("StartedAt = %s, want the impact start 2026-07-24", got)
	}

	active := byID["93999999"]
	if active.Status != StatusOpen {
		t.Errorf("active status = %q, want open", active.Status)
	}
	if !active.ResolvedAt.IsZero() {
		t.Error("an active incident must not have a resolution time")
	}
	if active.Impact != "major" {
		t.Errorf("impact = %q, want major", active.Impact)
	}

	// Only the live major incident drives the indicator.
	if res.Indicator != IndicatorMajor {
		t.Errorf("Indicator = %q, want major", res.Indicator)
	}
}

func TestHumanizeCamel(t *testing.T) {
	tests := map[string]string{
		"featurePerfDegradation": "Feature Perf Degradation",
		"serviceDisruption":      "Service Disruption",
		"einsteinBots":           "Einstein Bots",
		"":                       "",
	}
	for in, want := range tests {
		if got := humanizeCamel(in); got != want {
			t.Errorf("humanizeCamel(%q) = %q, want %q", in, got, want)
		}
	}
}
