package store

import (
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open("file:" + path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedProvider(t *testing.T, db *DB, id, endpoint string, tier int) {
	t.Helper()
	if err := db.UpsertProvider(Provider{
		ID: id, Name: id, Category: "test", PageURL: "https://" + id,
		Adapter: "statuspage", Endpoint: endpoint, Tier: tier, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
}

func seedCheck(t *testing.T, db *DB, endpoint, indicator string, ok bool, age time.Duration) {
	t.Helper()
	if err := db.UpsertCheck(Check{
		Endpoint:  endpoint,
		TS:        time.Now().UTC().Add(-age).Format(time.RFC3339),
		Indicator: indicator,
		OK:        ok,
		HTTPCode:  200,
	}); err != nil {
		t.Fatalf("UpsertCheck: %v", err)
	}
}

// GetOutages previously put LIMIT 1 inside the subquery, so it returned one row
// for the entire fleet no matter how many providers were down.
func TestGetOutagesReturnsEveryDownProvider(t *testing.T) {
	db := testDB(t)

	seedProvider(t, db, "alpha", "https://a/api", 1)
	seedProvider(t, db, "bravo", "https://b/api", 2)
	seedProvider(t, db, "charlie", "https://c/api", 3)
	seedProvider(t, db, "delta", "https://d/api", 4)

	seedCheck(t, db, "https://a/api", "critical", true, time.Minute)
	seedCheck(t, db, "https://b/api", "major", true, 2*time.Minute)
	seedCheck(t, db, "https://c/api", "unknown", false, 3*time.Minute)
	seedCheck(t, db, "https://d/api", "none", true, time.Minute)

	outages, err := db.GetOutages(30 * time.Minute)
	if err != nil {
		t.Fatalf("GetOutages: %v", err)
	}
	if len(outages) != 3 {
		names := make([]string, len(outages))
		for i, o := range outages {
			names[i] = o.ID
		}
		t.Fatalf("got %d outages %v, want 3 (alpha, bravo, charlie)", len(outages), names)
	}
	if outages[0].ID != "alpha" {
		t.Errorf("first outage = %s, want alpha (lowest tier first)", outages[0].ID)
	}
}

// The window used to be compared against SQLite's datetime() output, whose
// space separator sorts below RFC3339's 'T' - so anything from the same day
// matched regardless of the interval.
func TestGetOutagesRespectsWindow(t *testing.T) {
	db := testDB(t)
	seedProvider(t, db, "stale", "https://s/api", 1)
	seedCheck(t, db, "https://s/api", "critical", true, 3*time.Hour)

	outages, err := db.GetOutages(15 * time.Minute)
	if err != nil {
		t.Fatalf("GetOutages: %v", err)
	}
	if len(outages) != 0 {
		t.Errorf("got %d outages, want 0: the only check is 3h old", len(outages))
	}

	outages, err = db.GetOutages(6 * time.Hour)
	if err != nil {
		t.Fatalf("GetOutages: %v", err)
	}
	if len(outages) != 1 {
		t.Errorf("got %d outages, want 1 within a 6h window", len(outages))
	}
}

func TestGetOutagesUsesLatestCheckPerEndpoint(t *testing.T) {
	db := testDB(t)
	seedProvider(t, db, "recovered", "https://r/api", 1)
	seedCheck(t, db, "https://r/api", "critical", true, 20*time.Minute)
	seedCheck(t, db, "https://r/api", "none", true, time.Minute)

	outages, err := db.GetOutages(time.Hour)
	if err != nil {
		t.Fatalf("GetOutages: %v", err)
	}
	if len(outages) != 0 {
		t.Errorf("got %d outages, want 0: the most recent check is operational", len(outages))
	}
}

// SearchIncidents selected 10 columns but scanned 12, so it errored every time.
func TestSearchIncidentsWorks(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertIncident(Incident{
		ProviderID: "p1", ExtID: "i1",
		Title: "Elevated API error rates", Body: "Investigating elevated errors in the API tier.",
		Status: "investigating", StartedAt: "2026-09-01T10:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertIncident: %v", err)
	}

	got, err := db.SearchIncidents("elevated", 10)
	if err != nil {
		t.Fatalf("SearchIncidents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].ExtID != "i1" || got[0].FirstSeen == "" || got[0].LastSeen == "" {
		t.Errorf("unexpected incident: %+v", got[0])
	}

	if none, err := db.SearchIncidents("nonexistentterm", 10); err != nil || len(none) != 0 {
		t.Errorf("SearchIncidents(no match) = %v, %v; want empty, nil", none, err)
	}
}

// An incident whose provider reports "resolved" without a timestamp used to
// stay in the open list forever.
func TestGetOpenIncidentsExcludesResolvedStatus(t *testing.T) {
	db := testDB(t)

	mustUpsert := func(extID, status, resolvedAt string) {
		t.Helper()
		if err := db.UpsertIncident(Incident{
			ProviderID: "p1", ExtID: extID, Title: extID,
			Status: status, StartedAt: "2026-09-01T10:00:00Z", ResolvedAt: resolvedAt,
		}); err != nil {
			t.Fatalf("UpsertIncident(%s): %v", extID, err)
		}
	}

	mustUpsert("open1", "investigating", "")
	mustUpsert("open2", "monitoring", "")
	mustUpsert("closed1", "resolved", "")                          // status only
	mustUpsert("closed2", "investigating", "2026-09-01T12:00:00Z") // timestamp only
	mustUpsert("closed3", "resolved", "2026-09-01T12:00:00Z")      // both
	mustUpsert("zerotime", "resolved", "0001-01-01T00:00:00Z")     // Go zero time

	open, err := db.GetOpenIncidents()
	if err != nil {
		t.Fatalf("GetOpenIncidents: %v", err)
	}
	got := map[string]bool{}
	for _, inc := range open {
		got[inc.ExtID] = true
	}
	if len(open) != 2 || !got["open1"] || !got["open2"] {
		t.Errorf("open incidents = %v, want exactly open1 and open2", got)
	}
}

func TestUpsertIncidentNormalizesStatusCase(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertIncident(Incident{
		ProviderID: "p1", ExtID: "i1", Title: "t", Status: "Resolved",
		StartedAt: "2026-09-01T10:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertIncident: %v", err)
	}
	open, err := db.GetOpenIncidents()
	if err != nil {
		t.Fatalf("GetOpenIncidents: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("got %d open incidents, want 0: 'Resolved' must match 'resolved'", len(open))
	}
}

func TestGetLatestChecksReturnsNewestPerEndpoint(t *testing.T) {
	db := testDB(t)
	seedCheck(t, db, "https://a/api", "none", true, 10*time.Minute)
	seedCheck(t, db, "https://a/api", "major", true, time.Minute)
	seedCheck(t, db, "https://b/api", "none", true, time.Minute)

	latest, err := db.GetLatestChecks()
	if err != nil {
		t.Fatalf("GetLatestChecks: %v", err)
	}
	if len(latest) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(latest))
	}
	if latest["https://a/api"].Indicator != "major" {
		t.Errorf("a = %q, want the newest check (major)", latest["https://a/api"].Indicator)
	}
}

func TestBuildSnapshotStats(t *testing.T) {
	db := testDB(t)

	seedProvider(t, db, "ok1", "https://ok1/api", 1)
	seedProvider(t, db, "deg1", "https://deg1/api", 1)
	seedProvider(t, db, "down1", "https://down1/api", 1)
	seedProvider(t, db, "maint1", "https://maint1/api", 1)
	seedProvider(t, db, "unk1", "https://unk1/api", 1)
	if err := db.UpsertProvider(Provider{
		ID: "manual1", Name: "manual1", Category: "test", PageURL: "https://m",
		Adapter: "manual", Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}

	seedCheck(t, db, "https://ok1/api", "none", true, time.Minute)
	seedCheck(t, db, "https://deg1/api", "minor", true, time.Minute)
	seedCheck(t, db, "https://down1/api", "critical", true, time.Minute)
	seedCheck(t, db, "https://maint1/api", "maintenance", true, time.Minute)
	seedCheck(t, db, "https://unk1/api", "unknown", false, time.Minute)

	snap, err := db.BuildSnapshot()
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	// "minor" used to fall into Outages, leaving Degraded permanently at 0.
	want := SnapshotStats{
		TotalProviders: 6, Operational: 1, Degraded: 1, Outages: 1,
		Maintenance: 1, Unknown: 1, Unmonitored: 1, OpenIncidents: 0,
	}
	if snap.Stats != want {
		t.Errorf("stats = %+v, want %+v", snap.Stats, want)
	}
}

// The ETag hashed BuiltAt, so it changed every cycle even when nothing had.
func TestETagIgnoresBuildTimeButTracksContent(t *testing.T) {
	base := WorldSnapshot{
		BuiltAt:   "2026-09-01T10:00:00Z",
		Providers: []SnapshotProvider{{ID: "a", Indicator: "none", OK: true}},
	}
	later := base
	later.BuiltAt = "2026-09-01T10:10:00Z"

	if base.ETag() != later.ETag() {
		t.Error("ETag changed when only built_at changed")
	}

	changed := base
	changed.Providers = []SnapshotProvider{{ID: "a", Indicator: "critical", OK: true}}
	if base.ETag() == changed.ETag() {
		t.Error("ETag did not change when a provider indicator changed")
	}
}

func TestPruneResolvedIncidents(t *testing.T) {
	db := testDB(t)
	old := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339)

	mustUpsert := func(extID, status, resolvedAt, lastSeen string) {
		t.Helper()
		if err := db.UpsertIncident(Incident{
			ProviderID: "p1", ExtID: extID, Title: extID, Status: status,
			StartedAt: "2025-01-01T00:00:00Z", ResolvedAt: resolvedAt,
			FirstSeen: lastSeen, LastSeen: lastSeen,
		}); err != nil {
			t.Fatalf("UpsertIncident: %v", err)
		}
	}

	mustUpsert("oldResolved", "resolved", "2025-01-02T00:00:00Z", old)
	mustUpsert("oldOpen", "investigating", "", old)
	mustUpsert("recentResolved", "resolved", "2026-09-01T00:00:00Z", time.Now().UTC().Format(time.RFC3339))

	n, err := db.PruneResolvedIncidents(90 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneResolvedIncidents: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1 (only the old resolved incident)", n)
	}

	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM incidents").Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 2 {
		t.Errorf("%d incidents remain, want 2", remaining)
	}
}

func TestProviderNotesTolerateNull(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO providers (id, name, category, page_url, adapter, endpoint, tier, enabled, notes)
		VALUES ('n1', 'n1', 'test', 'https://n', 'manual', NULL, 4, 1, NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// A NULL notes column used to fail the scan and abort the whole listing.
	providers, err := db.GetProviders(ProviderFilter{})
	if err != nil {
		t.Fatalf("GetProviders: %v", err)
	}
	if len(providers) != 1 || providers[0].Notes != "" {
		t.Errorf("providers = %+v, want one row with empty notes", providers)
	}
	if _, err := db.GetProvider("n1"); err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
}
