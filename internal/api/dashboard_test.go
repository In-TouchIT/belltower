package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccarson/belltower/internal/store"
)

// Incident titles and provider names come from third-party status feeds. The
// snapshot must carry them verbatim (it is JSON), and the dashboard must not
// interpolate them as markup - the template ships an esc()/safeURL() pair and
// uses no inline onclick handlers.
func TestDashboardEscapesUntrustedContent(t *testing.T) {
	db, err := store.Open("file:" + filepath.Join(t.TempDir(), "xss.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	payload := `<img src=x onerror="alert(1)">`
	if err := db.UpsertProvider(store.Provider{
		ID: "evil", Name: payload, Category: "test", PageURL: "javascript:alert(1)",
		Adapter: "statuspage", Endpoint: "https://e/api", Tier: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCheck(store.Check{
		Endpoint: "https://e/api", TS: time.Now().UTC().Format(time.RFC3339),
		Indicator: "critical", OK: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertIncident(store.Incident{
		ProviderID: "evil", ExtID: "x1", Title: payload, Body: payload,
		Status: "investigating", StartedAt: "2026-09-01T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	s := NewServer(db, Config{Addr: ":0"})
	if err := s.refreshSnapshot(); err != nil {
		t.Fatal(err)
	}

	// The template itself must not contain an inline event handler and must
	// route untrusted values through the escapers.
	for _, required := range []string{"function esc(", "function safeURL(", "data-page-url"} {
		if !strings.Contains(dashboardTemplate, required) {
			t.Errorf("dashboard template missing %q", required)
		}
	}
	if strings.Contains(dashboardTemplate, "onclick=") {
		t.Error("dashboard template still uses an inline onclick handler")
	}
	// Values must reach the DOM only via esc()/safeURL(), never raw.
	for _, forbidden := range []string{"${p.name}", "${inc.title}", "${p.category}", "${cat}", "${inc.body}", "${p.page_url}"} {
		if strings.Contains(dashboardTemplate, forbidden) {
			t.Errorf("dashboard interpolates %s without escaping", forbidden)
		}
	}
}
