package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/In-TouchIT/belltower/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler, *store.DB) {
	t.Helper()
	db, err := store.Open("file:" + filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer(db, Config{Addr: ":0"})
	return s, s.Routes(), db
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func seed(t *testing.T, db *store.DB, id, endpoint, indicator string, ok bool) {
	t.Helper()
	if err := db.UpsertProvider(store.Provider{
		ID: id, Name: id, Category: "test", PageURL: "https://" + id,
		Adapter: "statuspage", Endpoint: endpoint, Tier: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := db.UpsertCheck(store.Check{
		Endpoint: endpoint, TS: time.Now().UTC().Format(time.RFC3339),
		Indicator: indicator, OK: ok, HTTPCode: 200,
	}); err != nil {
		t.Fatalf("UpsertCheck: %v", err)
	}
}

// "/" is a catch-all in net/http, so an unknown path used to render the
// dashboard with a 200.
func TestUnknownPathIs404(t *testing.T) {
	_, h, _ := testServer(t)
	if rec := get(t, h, "/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /does-not-exist = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", rec.Code)
	}
}

func TestJSONEndpointsSetContentType(t *testing.T) {
	s, h, db := testServer(t)
	seed(t, db, "alpha", "https://a/api", "none", true)
	if err := s.refreshSnapshot(); err != nil {
		t.Fatalf("refreshSnapshot: %v", err)
	}

	for _, path := range []string{
		"/api/v1/snapshot", "/api/v1/outages", "/api/v1/providers",
		"/api/v1/incidents", "/api/v1/changes", "/api/v1/health",
		"/api/v1/providers/alpha", "/openapi.json",
	} {
		rec := get(t, h, path)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s Content-Type = %q, want application/json", path, ct)
		}
	}
}

// Prometheus rejects a scrape that repeats HELP for one metric family, which
// is what emitting it inside the provider loop produced.
func TestMetricsEmitsHelpOncePerFamily(t *testing.T) {
	_, h, db := testServer(t)
	seed(t, db, "alpha", "https://a/api", "none", true)
	seed(t, db, "bravo", "https://b/api", "critical", true)
	seed(t, db, "charlie", "https://c/api", "unknown", false)

	body := get(t, h, "/metrics").Body.String()

	counts := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			counts[fields[1]+" "+fields[2]]++
		}
	}
	for key, n := range counts {
		if n != 1 {
			t.Errorf("%q appears %d times, want 1", key, n)
		}
	}

	if !strings.Contains(body, `belltower_provider_up{provider="alpha"`) {
		t.Error("missing alpha provider metric")
	}
	if !strings.Contains(body, `belltower_provider_up{provider="bravo",category="test",adapter="statuspage",indicator="critical"} 0`) {
		t.Errorf("bravo should report up=0:\n%s", body)
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	_, h, db := testServer(t)
	if err := db.UpsertProvider(store.Provider{
		ID: "weird", Name: `Acme "Quoted" \ Corp`, Category: "test",
		PageURL: "https://x", Adapter: "statuspage", Endpoint: "https://x/api",
		Tier: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := db.UpsertCheck(store.Check{
		Endpoint: "https://x/api", TS: time.Now().UTC().Format(time.RFC3339),
		Indicator: "none", OK: true,
	}); err != nil {
		t.Fatalf("UpsertCheck: %v", err)
	}

	body := get(t, h, "/metrics").Body.String()
	if strings.Contains(body, `provider="Acme "Quoted"`) {
		t.Errorf("unescaped quote leaked into a label:\n%s", body)
	}
	if !strings.Contains(body, `Acme \"Quoted\"`) {
		t.Errorf("expected escaped quotes in output:\n%s", body)
	}
}

func TestOutagesReportsAllDownProviders(t *testing.T) {
	_, h, db := testServer(t)
	seed(t, db, "alpha", "https://a/api", "critical", true)
	seed(t, db, "bravo", "https://b/api", "major", true)
	seed(t, db, "charlie", "https://c/api", "none", true)

	rec := get(t, h, "/api/v1/outages")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var outages []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &outages); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(outages) != 2 {
		t.Errorf("got %d outages, want 2: %s", len(outages), rec.Body.String())
	}
}

func TestIncidentSearchReturnsResults(t *testing.T) {
	_, h, db := testServer(t)
	if err := db.UpsertIncident(store.Incident{
		ProviderID: "p1", ExtID: "i1", Title: "Elevated error rates",
		Body: "Investigating.", Status: "investigating", StartedAt: "2026-09-01T10:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertIncident: %v", err)
	}

	rec := get(t, h, "/api/v1/incidents?q=elevated")
	if rec.Code != http.StatusOK {
		t.Fatalf("search returned %d: %s", rec.Code, rec.Body.String())
	}
	var incidents []store.Incident
	if err := json.Unmarshal(rec.Body.Bytes(), &incidents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(incidents) != 1 {
		t.Errorf("got %d incidents, want 1", len(incidents))
	}
}

func TestSnapshotETagReturns304(t *testing.T) {
	s, h, db := testServer(t)
	seed(t, db, "alpha", "https://a/api", "none", true)
	if err := s.refreshSnapshot(); err != nil {
		t.Fatalf("refreshSnapshot: %v", err)
	}

	first := get(t, h, "/api/v1/snapshot")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag header")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", rec.Code)
	}

	// A rebuild with unchanged data must keep the same ETag.
	if err := s.refreshSnapshot(); err != nil {
		t.Fatalf("refreshSnapshot: %v", err)
	}
	if got := get(t, h, "/api/v1/snapshot").Header().Get("ETag"); got != etag {
		t.Errorf("ETag changed across an unchanged rebuild: %s -> %s", etag, got)
	}
}

func TestChangesReportsTransitions(t *testing.T) {
	_, h, db := testServer(t)
	if err := db.UpsertProvider(store.Provider{
		ID: "alpha", Name: "Alpha", Category: "test", PageURL: "https://a",
		Adapter: "statuspage", Endpoint: "https://a/api", Tier: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	for i, ind := range []string{"none", "none", "critical"} {
		if err := db.UpsertCheck(store.Check{
			Endpoint:  "https://a/api",
			TS:        time.Now().UTC().Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			Indicator: ind, OK: true,
		}); err != nil {
			t.Fatalf("UpsertCheck: %v", err)
		}
	}

	rec := get(t, h, "/api/v1/changes?since=2020-01-01T00:00:00Z")
	var changes []struct {
		Providers []string `json:"providers"`
		Old       string   `json:"old_indicator"`
		New       string   `json:"new_indicator"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &changes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Only the none -> critical transition is a change.
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %s", len(changes), rec.Body.String())
	}
	if changes[0].Old != "none" || changes[0].New != "critical" {
		t.Errorf("change = %+v, want none -> critical", changes[0])
	}
	if len(changes[0].Providers) != 1 || changes[0].Providers[0] != "Alpha" {
		t.Errorf("providers = %v, want [Alpha]", changes[0].Providers)
	}
}

func TestBadQueryParamsAreRejected(t *testing.T) {
	_, h, _ := testServer(t)
	if rec := get(t, h, "/api/v1/incidents?limit=abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad limit = %d, want 400", rec.Code)
	}
	if rec := get(t, h, "/api/v1/outages?within=notaduration"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad within = %d, want 400", rec.Code)
	}
	if rec := get(t, h, "/api/v1/providers/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("missing provider = %d, want 404", rec.Code)
	}
}
