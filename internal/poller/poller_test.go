package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/In-TouchIT/belltower/internal/adapters"
	"github.com/In-TouchIT/belltower/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open("file:" + filepath.Join(t.TempDir(), "poller.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFormatTimeMapsZeroToEmpty(t *testing.T) {
	if got := formatTime(time.Time{}); got != "" {
		t.Errorf("formatTime(zero) = %q, want \"\" so it stores as NULL", got)
	}
	ts := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if got := formatTime(ts); got != "2026-09-01T10:00:00Z" {
		t.Errorf("formatTime = %q", got)
	}
}

func TestPruneCycles(t *testing.T) {
	tests := []struct {
		interval time.Duration
		want     int64
	}{
		{10 * time.Minute, 144},
		{time.Hour, 24},
		{48 * time.Hour, 1}, // longer than a day still prunes every cycle
		{0, 1},
	}
	for _, tt := range tests {
		if got := pruneCycles(tt.interval); got != tt.want {
			t.Errorf("pruneCycles(%v) = %d, want %d", tt.interval, got, tt.want)
		}
	}
}

// A provider that cannot be reached must be recorded as unknown, never as
// operational, and must not wipe out previously recorded incidents.
func TestPollRecordsUnknownOnFailure(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	if err := db.UpsertProvider(store.Provider{
		ID: "alpha", Name: "Alpha", Category: "test", PageURL: srv.URL,
		Adapter: "statuspage", Endpoint: srv.URL, Tier: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}

	p := New(db, adapters.NewRegistry("belltower-test", 2*time.Second), Config{
		Interval: time.Minute, Timeout: 2 * time.Second, Concurrency: 2,
	})
	if err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	latest, err := db.GetLatestChecks()
	if err != nil {
		t.Fatalf("GetLatestChecks: %v", err)
	}
	check, ok := latest[srv.URL]
	if !ok {
		t.Fatal("no check recorded")
	}
	if check.Indicator != "unknown" {
		t.Errorf("indicator = %q, want unknown", check.Indicator)
	}
	if check.OK {
		t.Error("ok should be false for a failed poll")
	}
	if check.HTTPCode != http.StatusForbidden {
		t.Errorf("http_code = %d, want 403 (it used to be hardcoded 200)", check.HTTPCode)
	}
	if check.Err == "" {
		t.Error("error message should be recorded")
	}
}

const summaryJSON = `{
  "page": {"id": "p"},
  "incidents": [{"id": "i1", "name": "Live incident", "status": "investigating", "impact": "major",
                 "created_at": "2026-09-01T10:00:00Z", "updated_at": "2026-09-01T10:00:00Z"}],
  "components": [{"id": "c1", "name": "API", "status": "partial_outage", "updated_at": "2026-09-01T10:00:00Z"}]
}`

// Providers sharing one endpoint are polled once but must each get their own
// incident and component rows - deduplicating them away meant only the first
// provider ever recorded anything.
func TestSharedEndpointRecordsAllProviders(t *testing.T) {
	db := testDB(t)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(summaryJSON))
	}))
	defer srv.Close()

	for _, id := range []string{"alpha", "bravo"} {
		if err := db.UpsertProvider(store.Provider{
			ID: id, Name: id, Category: "test", PageURL: srv.URL,
			Adapter: "statuspage", Endpoint: srv.URL, Tier: 1, Enabled: true,
		}); err != nil {
			t.Fatalf("UpsertProvider: %v", err)
		}
	}

	p := New(db, adapters.NewRegistry("belltower-test", 2*time.Second), Config{
		Interval: time.Minute, Timeout: 2 * time.Second, Concurrency: 2,
	})
	if err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if hits != 1 {
		t.Errorf("made %d requests, want 1 for a shared endpoint", hits)
	}

	open, err := db.GetOpenIncidents()
	if err != nil {
		t.Fatalf("GetOpenIncidents: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("got %d incidents, want one per provider sharing the endpoint", len(open))
	}

	var components int
	if err := db.QueryRow("SELECT COUNT(*) FROM components").Scan(&components); err != nil {
		t.Fatalf("count components: %v", err)
	}
	if components != 2 {
		t.Errorf("got %d components, want 2", components)
	}

	// A partial_outage component must not read as operational.
	latest, err := db.GetLatestChecks()
	if err != nil {
		t.Fatalf("GetLatestChecks: %v", err)
	}
	if got := latest[srv.URL].Indicator; got != "major" {
		t.Errorf("indicator = %q, want major", got)
	}
}

func TestManualProvidersAreNotPolled(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertProvider(store.Provider{
		ID: "manual1", Name: "Manual", Category: "test", PageURL: "https://x",
		Adapter: "manual", Endpoint: "", Tier: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}

	p := New(db, adapters.NewRegistry("t", time.Second), Config{Interval: time.Minute, Concurrency: 1})
	if err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var checks int
	if err := db.QueryRow("SELECT COUNT(*) FROM checks").Scan(&checks); err != nil {
		t.Fatalf("count: %v", err)
	}
	if checks != 0 {
		t.Errorf("recorded %d checks for a manual provider, want 0", checks)
	}
}
