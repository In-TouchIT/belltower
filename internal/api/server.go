package api

import (
	"compress/gzip"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/In-TouchIT/belltower/internal/store"
)

// Server is the HTTP server for the status page monitor
type Server struct {
	db   *store.DB
	addr string

	dashboard *template.Template

	mu       sync.RWMutex
	snapshot *snapshotCache
}

// snapshotCache holds the most recent snapshot in memory
type snapshotCache struct {
	data      []byte
	etag      string
	timestamp time.Time
}

// Config holds server configuration
type Config struct {
	Addr string
}

// NewServer creates a new API server.
// The dashboard template is parsed once here rather than per request.
func NewServer(db *store.DB, cfg Config) *Server {
	return &Server{
		db:        db,
		addr:      cfg.Addr,
		dashboard: template.Must(template.New("dashboard").Parse(dashboardTemplate)),
	}
}

//go:embed templates/dashboard.html
var dashboardTemplate string

//go:embed static/favicon.png
var faviconBytes []byte

// Routes sets up all HTTP routes
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/favicon.png", s.handleFavicon)
	mux.HandleFunc("/api/v1/snapshot", s.handleSnapshot)
	mux.HandleFunc("/api/v1/outages", s.handleOutages)
	mux.HandleFunc("/api/v1/providers", s.handleProviders)
	mux.HandleFunc("/api/v1/providers/", s.handleProvider)
	mux.HandleFunc("/api/v1/incidents", s.handleIncidents)
	mux.HandleFunc("/api/v1/changes", s.handleChanges)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("/metrics", s.handleMetrics)

	// "/" is a catch-all in net/http, so unknown paths must be rejected
	// explicitly - otherwise every typo returns the dashboard with a 200.
	mux.HandleFunc("/", s.handleDashboard)

	return mux
}

// writeJSON encodes v as JSON with the correct content type.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleSnapshot serves the precomputed snapshot (memory read, no queries)
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snap := s.snapshot
	s.mu.RUnlock()

	if snap == nil {
		s.refreshSnapshot()
		s.mu.RLock()
		snap = s.snapshot
		s.mu.RUnlock()
	}

	if snap == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "no snapshot available")
		return
	}

	w.Header().Set("ETag", snap.etag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, snap.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		if _, err := gz.Write(snap.data); err != nil {
			log.Printf("failed to write gzipped snapshot: %v", err)
		}
		return
	}

	if _, err := w.Write(snap.data); err != nil {
		log.Printf("failed to write snapshot: %v", err)
	}
}

// etagMatches implements If-None-Match, which may carry a comma-separated list.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

// handleOutages returns providers whose most recent check is non-operational.
func (s *Server) handleOutages(w http.ResponseWriter, r *http.Request) {
	window := 30 * time.Minute
	if raw := r.URL.Query().Get("within"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid 'within' duration: "+raw)
			return
		}
		window = parsed
	}
	category := r.URL.Query().Get("category")

	providers, err := s.db.GetOutages(window)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	latest, err := s.db.GetLatestChecks()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type outage struct {
		ID        string `json:"id"`
		Provider  string `json:"provider"`
		Category  string `json:"category"`
		Adapter   string `json:"adapter"`
		Indicator string `json:"indicator"`
		Reachable bool   `json:"reachable"`
		Tier      int    `json:"tier"`
		Endpoint  string `json:"endpoint"`
		PageURL   string `json:"page_url"`
		LastCheck string `json:"last_check,omitempty"`
		Error     string `json:"error,omitempty"`
	}

	outages := []outage{}
	for _, p := range providers {
		if category != "" && p.Category != category {
			continue
		}
		check := latest[p.Endpoint]
		outages = append(outages, outage{
			ID:        p.ID,
			Provider:  p.Name,
			Category:  p.Category,
			Adapter:   p.Adapter,
			Indicator: check.Indicator,
			Reachable: check.OK,
			Tier:      p.Tier,
			Endpoint:  p.Endpoint,
			PageURL:   p.PageURL,
			LastCheck: check.TS,
			Error:     check.Err,
		})
	}

	writeJSON(w, http.StatusOK, outages)
}

// handleProviders returns the full provider inventory
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.db.GetProviders(store.ProviderFilter{
		Adapter:     r.URL.Query().Get("adapter"),
		Category:    r.URL.Query().Get("category"),
		EnabledOnly: r.URL.Query().Get("enabled_only") == "true",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// One query for all latest checks instead of one per provider.
	latest, err := s.db.GetLatestChecks()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type providerResponse struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Category  string `json:"category"`
		Adapter   string `json:"adapter"`
		Tier      int    `json:"tier"`
		Enabled   bool   `json:"enabled"`
		PageURL   string `json:"page_url,omitempty"`
		Endpoint  string `json:"endpoint,omitempty"`
		Indicator string `json:"indicator,omitempty"`
		Reachable bool   `json:"reachable"`
		LastCheck string `json:"last_check,omitempty"`
		Notes     string `json:"notes,omitempty"`
	}

	resp := make([]providerResponse, 0, len(providers))
	for _, p := range providers {
		pr := providerResponse{
			ID:       p.ID,
			Name:     p.Name,
			Category: p.Category,
			Adapter:  p.Adapter,
			Tier:     p.Tier,
			Enabled:  p.Enabled,
			PageURL:  p.PageURL,
			Endpoint: p.Endpoint,
			Notes:    p.Notes,
		}
		if check, ok := latest[p.Endpoint]; ok && p.Endpoint != "" {
			pr.Indicator = check.Indicator
			pr.LastCheck = check.TS
			pr.Reachable = check.OK
		}
		resp = append(resp, pr)
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleProvider returns details for a single provider
func (s *Server) handleProvider(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/providers/")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "provider id required")
		return
	}

	p, err := s.db.GetProvider(id)
	if err == sql.ErrNoRows {
		writeJSONError(w, http.StatusNotFound, "provider not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var check store.Check
	if p.Endpoint != "" {
		var okInt int
		var errMsg sql.NullString
		scanErr := s.db.QueryRow(`
			SELECT ts, COALESCE(http_code, 0), COALESCE(latency_ms, 0), COALESCE(indicator, ''), COALESCE(ok, 0), err
			FROM checks WHERE endpoint = ?
			ORDER BY ts DESC LIMIT 1
		`, p.Endpoint).Scan(&check.TS, &check.HTTPCode, &check.LatencyMS, &check.Indicator, &okInt, &errMsg)
		if scanErr != nil && scanErr != sql.ErrNoRows {
			writeJSONError(w, http.StatusInternalServerError, scanErr.Error())
			return
		}
		check.OK = okInt == 1
		check.Err = errMsg.String
	}

	openIncidents, err := s.db.GetOpenIncidents()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	providerIncidents := []store.Incident{}
	for _, inc := range openIncidents {
		if inc.ProviderID == p.ID {
			providerIncidents = append(providerIncidents, inc)
		}
	}

	components, err := s.providerComponents(p.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	history, err := s.checkHistory(p.Endpoint)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":            p.ID,
		"name":          p.Name,
		"category":      p.Category,
		"adapter":       p.Adapter,
		"tier":          p.Tier,
		"enabled":       p.Enabled,
		"page_url":      p.PageURL,
		"endpoint":      p.Endpoint,
		"notes":         p.Notes,
		"indicator":     check.Indicator,
		"reachable":     check.OK,
		"last_check":    check.TS,
		"last_error":    check.Err,
		"incidents":     providerIncidents,
		"components":    components,
		"check_history": history,
	})
}

func (s *Server) providerComponents(providerID string) ([]store.Component, error) {
	rows, err := s.db.Query(`
		SELECT name, COALESCE(status, ''), COALESCE(updated_at, '')
		FROM components WHERE provider_id = ? ORDER BY name
	`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	components := []store.Component{}
	for rows.Next() {
		var c store.Component
		if err := rows.Scan(&c.Name, &c.Status, &c.UpdatedAt); err != nil {
			return nil, err
		}
		components = append(components, c)
	}
	return components, rows.Err()
}

type checkHistoryEntry struct {
	TS        string `json:"ts"`
	Indicator string `json:"indicator"`
	LatencyMS int    `json:"latency_ms"`
	OK        bool   `json:"ok"`
}

func (s *Server) checkHistory(endpoint string) ([]checkHistoryEntry, error) {
	history := []checkHistoryEntry{}
	if endpoint == "" {
		return history, nil
	}

	rows, err := s.db.Query(`
		SELECT ts, COALESCE(indicator, ''), COALESCE(latency_ms, 0), COALESCE(ok, 0)
		FROM checks WHERE endpoint = ?
		ORDER BY ts DESC LIMIT 100
	`, endpoint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var e checkHistoryEntry
		var okInt int
		if err := rows.Scan(&e.TS, &e.Indicator, &e.LatencyMS, &okInt); err != nil {
			return nil, err
		}
		e.OK = okInt == 1
		history = append(history, e)
	}
	return history, rows.Err()
}

// handleIncidents returns incidents with search capability
func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid 'limit': "+raw)
			return
		}
		if parsed > 500 {
			parsed = 500
		}
		limit = parsed
	}

	if query := r.URL.Query().Get("q"); query != "" {
		incidents, err := s.db.SearchIncidents(query, limit)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "search failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nonNilIncidents(incidents))
		return
	}

	var args []interface{}
	var where []string
	if impact := r.URL.Query().Get("impact"); impact != "" {
		where = append(where, "impact = ?")
		args = append(args, impact)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		where = append(where, "status = ?")
		args = append(args, strings.ToLower(status))
	}
	if since := r.URL.Query().Get("since"); since != "" {
		where = append(where, "started_at >= ?")
		args = append(args, since)
	}
	if r.URL.Query().Get("open") == "true" {
		where = append(where, "(resolved_at IS NULL OR resolved_at = '')")
		where = append(where, "LOWER(COALESCE(status, '')) NOT IN ('resolved', 'closed', 'completed', 'postmortem')")
	}

	query := `SELECT provider_id, ext_id, title, COALESCE(impact, ''), COALESCE(status, ''), COALESCE(started_at, ''),
	                 resolved_at, COALESCE(url, ''), COALESCE(body, ''), raw_json, first_seen, last_seen
	          FROM incidents`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY started_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	incidents, err := store.ScanIncidents(rows)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, nonNilIncidents(incidents))
}

func nonNilIncidents(in []store.Incident) []store.Incident {
	if in == nil {
		return []store.Incident{}
	}
	return in
}

// handleChanges returns actual indicator transitions since a timestamp.
//
// This compares each check against the one before it for the same endpoint and
// reports only the rows where the indicator differs - previously it returned
// every check with an always-empty old_indicator, which reported no changes at
// all no matter what had happened.
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	}

	rows, err := s.db.Query(`
		SELECT endpoint, ts, indicator, prev_indicator FROM (
			SELECT c.endpoint,
			       c.ts,
			       COALESCE(c.indicator, '') AS indicator,
			       COALESCE(LAG(c.indicator) OVER (PARTITION BY c.endpoint ORDER BY c.ts), '') AS prev_indicator
			FROM checks c
		)
		WHERE ts >= ? AND prev_indicator != '' AND indicator != prev_indicator
		ORDER BY ts DESC
		LIMIT 200
	`, since)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	// Endpoints are shared between providers, so resolve names in bulk.
	providers, err := s.db.GetProviders(store.ProviderFilter{})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	namesByEndpoint := make(map[string][]string)
	for _, p := range providers {
		if p.Endpoint != "" {
			namesByEndpoint[p.Endpoint] = append(namesByEndpoint[p.Endpoint], p.Name)
		}
	}

	type change struct {
		Providers []string `json:"providers"`
		Endpoint  string   `json:"endpoint"`
		Old       string   `json:"old_indicator"`
		New       string   `json:"new_indicator"`
		Timestamp string   `json:"timestamp"`
	}

	changes := []change{}
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.Endpoint, &c.Timestamp, &c.New, &c.Old); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.Providers = namesByEndpoint[c.Endpoint]
		sort.Strings(c.Providers)
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, changes)
}

// handleHealth returns the service's own health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snap := s.snapshot
	s.mu.RUnlock()

	health := map[string]interface{}{
		"status": "operational",
	}

	if snap == nil {
		health["status"] = "starting"
		health["stale_seconds"] = nil
		writeJSON(w, http.StatusServiceUnavailable, health)
		return
	}

	staleness := time.Since(snap.timestamp)
	health["last_snapshot"] = snap.timestamp.UTC().Format(time.RFC3339)
	health["stale_seconds"] = int(staleness.Seconds())
	health["snapshot_etag"] = snap.etag

	status := http.StatusOK
	if staleness > 30*time.Minute {
		health["status"] = "degraded"
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, health)
}

// handleOpenAPI returns the OpenAPI specification
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	io.WriteString(w, OpenAPISpec)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(faviconBytes)
}

// escapePromLabel escapes a Prometheus label value per the exposition format.
func escapePromLabel(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

// handleMetrics returns Prometheus metrics.
//
// HELP/TYPE lines are emitted exactly once per metric family: repeating them
// inside the provider loop (as this used to) makes Prometheus reject the whole
// scrape with "second HELP line for metric name".
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	var b strings.Builder

	b.WriteString("# HELP belltower_up Whether the belltower service is running\n")
	b.WriteString("# TYPE belltower_up gauge\n")
	b.WriteString("belltower_up 1\n")

	b.WriteString("# HELP belltower_snapshot_stale_seconds Age of the last snapshot in seconds\n")
	b.WriteString("# TYPE belltower_snapshot_stale_seconds gauge\n")
	s.mu.RLock()
	snap := s.snapshot
	s.mu.RUnlock()
	if snap != nil {
		fmt.Fprintf(&b, "belltower_snapshot_stale_seconds %.0f\n", time.Since(snap.timestamp).Seconds())
	} else {
		b.WriteString("belltower_snapshot_stale_seconds NaN\n")
	}

	providers, err := s.db.GetProviders(store.ProviderFilter{EnabledOnly: true})
	if err != nil {
		log.Printf("metrics: failed to load providers: %v", err)
		io.WriteString(w, b.String())
		return
	}
	latest, err := s.db.GetLatestChecks()
	if err != nil {
		log.Printf("metrics: failed to load checks: %v", err)
		io.WriteString(w, b.String())
		return
	}

	b.WriteString("# HELP belltower_provider_up Whether a provider reports itself operational\n")
	b.WriteString("# TYPE belltower_provider_up gauge\n")
	for _, p := range providers {
		if p.Endpoint == "" {
			continue
		}
		check, ok := latest[p.Endpoint]
		if !ok {
			continue
		}
		up := 0
		if check.Indicator == store.IndicatorNone {
			up = 1
		}
		fmt.Fprintf(&b, `belltower_provider_up{provider="%s",category="%s",adapter="%s",indicator="%s"} %d`+"\n",
			escapePromLabel(p.Name), escapePromLabel(p.Category), escapePromLabel(p.Adapter),
			escapePromLabel(check.Indicator), up)
	}

	b.WriteString("# HELP belltower_provider_reachable Whether the last poll reached the provider\n")
	b.WriteString("# TYPE belltower_provider_reachable gauge\n")
	for _, p := range providers {
		if p.Endpoint == "" {
			continue
		}
		check, ok := latest[p.Endpoint]
		if !ok {
			continue
		}
		reachable := 0
		if check.OK {
			reachable = 1
		}
		fmt.Fprintf(&b, `belltower_provider_reachable{provider="%s",adapter="%s"} %d`+"\n",
			escapePromLabel(p.Name), escapePromLabel(p.Adapter), reachable)
	}

	b.WriteString("# HELP belltower_provider_latency_ms Latency of the last poll in milliseconds\n")
	b.WriteString("# TYPE belltower_provider_latency_ms gauge\n")
	for _, p := range providers {
		if p.Endpoint == "" {
			continue
		}
		if check, ok := latest[p.Endpoint]; ok {
			fmt.Fprintf(&b, `belltower_provider_latency_ms{provider="%s"} %d`+"\n",
				escapePromLabel(p.Name), check.LatencyMS)
		}
	}

	io.WriteString(w, b.String())
}

// handleDashboard serves the HTML dashboard, and 404s everything else.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.dashboard.Execute(w, nil); err != nil {
		log.Printf("failed to render dashboard: %v", err)
	}
}

// RefreshSnapshot rebuilds the in-memory snapshot cache
func (s *Server) RefreshSnapshot(ctx context.Context) error {
	return s.refreshSnapshot()
}

func (s *Server) refreshSnapshot() error {
	snapshot, err := s.db.BuildSnapshot()
	if err != nil {
		return fmt.Errorf("failed to build snapshot: %w", err)
	}

	jsonData, err := snapshot.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = &snapshotCache{
		data:      jsonData,
		etag:      snapshot.ETag(),
		timestamp: time.Now(),
	}
	return nil
}

// OpenAPISpec is the embedded OpenAPI specification
const OpenAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Belltower Status Page Monitor API",
    "version": "1.0.0",
    "description": "Aggregates vendor status pages into a single dashboard and API, answering 'is this us, or is <vendor> down?' in one place."
  },
  "servers": [
    {"url": "http://192.168.111.122:8088"}
  ],
  "components": {
    "schemas": {
      "Indicator": {
        "type": "string",
        "enum": ["none", "minor", "major", "critical", "maintenance", "unknown"],
        "description": "Operational status: none=operational, minor/degraded, major/critical outage, maintenance=planned, unknown=unreachable"
      },
      "Provider": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "Unique slug identifier"},
          "name": {"type": "string"},
          "category": {"type": "string"},
          "adapter": {"type": "string"},
          "endpoint": {"type": "string", "nullable": true, "description": "API endpoint URL (null for manual providers)"},
          "page_url": {"type": "string"},
          "tier": {"type": "integer", "description": "1=critical infrastructure, 2=business-critical, 3=important, 4=informational"},
          "enabled": {"type": "boolean"},
          "indicator": {"$ref": "#/components/schemas/Indicator"},
          "reachable": {"type": "boolean", "description": "Whether the last poll got an HTTP response"},
          "last_check": {"type": "string", "format": "date-time", "nullable": true},
          "notes": {"type": "string", "nullable": true}
        },
        "required": ["id", "name", "category", "adapter", "indicator", "reachable"]
      },
      "ProviderDetail": {
        "allOf": [
          {"$ref": "#/components/schemas/Provider"},
          {
            "type": "object",
            "properties": {
              "incidents": {"type": "array", "items": {"$ref": "#/components/schemas/Incident"}},
              "components": {"type": "array", "items": {"$ref": "#/components/schemas/Component"}},
              "check_history": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "ts": {"type": "string", "format": "date-time"},
                    "indicator": {"$ref": "#/components/schemas/Indicator"},
                    "latency_ms": {"type": "integer", "nullable": true},
                    "ok": {"type": "boolean"}
                  }
                }
              },
              "last_error": {"type": "string", "nullable": true}
            }
          }
        ]
      },
      "Incident": {
        "type": "object",
        "properties": {
          "provider_id": {"type": "string"},
          "ext_id": {"type": "string", "description": "External incident ID from the status page"},
          "title": {"type": "string"},
          "impact": {"type": "string", "description": "none|minor|major|critical|maintenance"},
          "status": {"type": "string", "description": "investigating|identified|monitoring|resolved|reported"},
          "started_at": {"type": "string", "format": "date-time"},
          "resolved_at": {"type": "string", "format": "date-time", "nullable": true},
          "url": {"type": "string", "nullable": true},
          "body": {"type": "string", "nullable": true},
          "first_seen": {"type": "string", "format": "date-time"},
          "last_seen": {"type": "string", "format": "date-time"}
        },
        "required": ["provider_id", "ext_id", "title", "impact", "status", "started_at", "first_seen", "last_seen"]
      },
      "Component": {
        "type": "object",
        "properties": {
          "provider_id": {"type": "string"},
          "name": {"type": "string"},
          "status": {"type": "string", "description": "operational|degraded|major|maintenance|unknown"},
          "updated_at": {"type": "string", "format": "date-time"}
        },
        "required": ["name", "status"]
      },
      "Snapshot": {
        "type": "object",
        "properties": {
          "built_at": {"type": "string", "format": "date-time"},
          "stats": {
            "type": "object",
            "properties": {
              "total_providers": {"type": "integer"},
              "operational": {"type": "integer"},
              "degraded": {"type": "integer"},
              "outage": {"type": "integer"},
              "maintenance": {"type": "integer"},
              "unknown": {"type": "integer"}
            }
          },
          "providers": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "id": {"type": "string"},
                "name": {"type": "string"},
                "category": {"type": "string"},
                "adapter": {"type": "string"},
                "indicator": {"$ref": "#/components/schemas/Indicator"},
                "ok": {"type": "boolean"},
                "tier": {"type": "integer"},
                "page_url": {"type": "string"},
                "last_check": {"type": "string", "format": "date-time", "nullable": true},
                "latency_ms": {"type": "integer", "nullable": true},
                "error": {"type": "string", "nullable": true}
              },
              "required": ["id", "name", "category", "indicator", "ok", "tier"]
            }
          },
          "incidents": {"type": "array", "items": {"$ref": "#/components/schemas/Incident"}},
          "components": {"type": "array", "items": {"$ref": "#/components/schemas/Component"}}
        },
        "required": ["built_at", "providers"]
      },
      "Health": {
        "type": "object",
        "properties": {
          "status": {"type": "string", "enum": ["operational", "degraded", "unhealthy"]},
          "last_cycle": {"type": "string", "format": "date-time", "nullable": true},
          "cycle_duration_ms": {"type": "integer", "nullable": true},
          "stale_seconds": {"type": "integer"},
          "snapshot_etag": {"type": "string"},
          "providers_checked": {"type": "integer"},
          "adapter_errors": {"type": "integer"},
          "unmonitored": {"type": "integer"}
        }
      },
      "Change": {
        "type": "object",
        "properties": {
          "providers": {"type": "array", "items": {"type": "string"}},
          "endpoint": {"type": "string"},
          "old_indicator": {"$ref": "#/components/schemas/Indicator"},
          "new_indicator": {"$ref": "#/components/schemas/Indicator"},
          "timestamp": {"type": "string", "format": "date-time"}
        },
        "required": ["providers", "old_indicator", "new_indicator", "timestamp"]
      }
    }
  },
  "paths": {
    "/api/v1/snapshot": {
      "get": {
        "summary": "Get full current state snapshot",
        "description": "Returns the precomputed snapshot representing the entire current state. Uses ETag for efficient caching - send If-None-Match to get 304 Not Modified when unchanged.",
        "parameters": [
          {"name": "If-None-Match", "in": "header", "schema": {"type": "string"}, "description": "ETag from a previous response"},
          {"name": "category", "in": "query", "schema": {"type": "string"}, "description": "Filter providers by category"}
        ],
        "responses": {
          "200": {
            "description": "Current snapshot",
            "headers": {"ETag": {"schema": {"type": "string"}}},
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Snapshot"}}}
          },
          "304": {
            "description": "Not modified (ETag match)",
            "headers": {"ETag": {"schema": {"type": "string"}}}
          }
        }
      }
    },
    "/api/v1/outages": {
      "get": {
        "summary": "Get current outages",
        "description": "Returns providers whose latest check is non-operational (not 'none' or 'maintenance').",
        "parameters": [
          {"name": "category", "in": "query", "schema": {"type": "string"}},
          {"name": "min_impact", "in": "query", "schema": {"type": "string"}, "description": "Filter by minimum impact level"},
          {"name": "within", "in": "query", "schema": {"type": "string"}, "description": "Only show outages within this duration (e.g. 30m, 2h)"}
        ],
        "responses": {
          "200": {
            "description": "List of outages",
            "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Provider"}}}}
          }
        }
      }
    },
    "/api/v1/providers": {
      "get": {
        "summary": "Get provider inventory",
        "parameters": [
          {"name": "adapter", "in": "query", "schema": {"type": "string"}, "description": "Filter by adapter type"},
          {"name": "category", "in": "query", "schema": {"type": "string"}},
          {"name": "enabled_only", "in": "query", "schema": {"type": "boolean"}, "description": "Exclude disabled providers"}
        ],
        "responses": {
          "200": {
            "description": "Provider list",
            "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Provider"}}}}
          }
        }
      }
    },
    "/api/v1/providers/{id}": {
      "get": {
        "summary": "Get provider details including incidents and components",
        "parameters": [
          {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {
            "description": "Provider details",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ProviderDetail"}}}
          },
          "404": {"description": "Provider not found"}
        }
      }
    },
    "/api/v1/incidents": {
      "get": {
        "summary": "Search incidents",
        "parameters": [
          {"name": "q", "in": "query", "schema": {"type": "string"}, "description": "Full-text search on title and body"},
          {"name": "since", "in": "query", "schema": {"type": "string", "format": "date-time"}},
          {"name": "impact", "in": "query", "schema": {"type": "string"}},
          {"name": "status", "in": "query", "schema": {"type": "string"}, "description": "incident status: investigating|identified|monitoring|resolved"},
          {"name": "provider_id", "in": "query", "schema": {"type": "string"}},
          {"name": "open", "in": "query", "schema": {"type": "boolean"}, "description": "Only open (unresolved) incidents"},
          {"name": "limit", "in": "query", "schema": {"type": "integer"}, "default": 50}
        ],
        "responses": {
          "200": {
            "description": "Incident list",
            "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Incident"}}}}
          }
        }
      }
    },
    "/api/v1/changes": {
      "get": {
        "summary": "Get recent changes since a timestamp",
        "description": "Returns provider indicator transitions since the given timestamp. Useful for standup/triage to see what changed.",
        "parameters": [
          {"name": "since", "in": "query", "schema": {"type": "string", "format": "date-time"}, "description": "ISO timestamp to compare against"}
        ],
        "responses": {
          "200": {
            "description": "Change list",
            "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Change"}}}}
          }
        }
      }
    },
    "/api/v1/health": {
      "get": {
        "summary": "Get service health",
        "description": "Returns the health of the belltower service itself - last poll cycle, adapter error counts, and snapshot staleness.",
        "responses": {
          "200": {
            "description": "Service is healthy",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Health"}}}
          },
          "503": {
            "description": "Service is unhealthy (stale snapshot or poll failures)",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Health"}}}
          }
        }
      }
    },
    "/metrics": {
      "get": {
        "summary": "Prometheus metrics",
        "description": "Prometheus-compatible metrics endpoint. Metrics use the 'belltower_' prefix.",
        "responses": {
          "200": {
            "description": "Metrics in Prometheus text format",
            "content": {"text/plain": {"schema": {"type": "string"}}}
          }
        }
      }
    },
    "/favicon.png": {
      "get": {
        "summary": "Belltower favicon",
        "description": "Favicon for the Belltower dashboard.",
        "responses": {
          "200": {
            "description": "Favicon image",
            "content": {"image/png": {"schema": {"type": "string", "format": "binary"}}}
          }
        }
      }
    }
  }
}`
