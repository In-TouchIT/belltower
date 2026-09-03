package api

import (
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ccarson/belltower/internal/adapters"
	"github.com/ccarson/belltower/internal/store"
)

// Server is the HTTP server for the status page monitor
type Server struct {
	db       *store.DB
	registry *adapters.Registry
	addr     string
	
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
	Addr      string
	UserAgent string
}

// NewServer creates a new API server
func NewServer(db *store.DB, registry *adapters.Registry, cfg Config) *Server {
	s := &Server{
		db:       db,
		registry: registry,
		addr:     cfg.Addr,
	}
	return s
}

// Routes sets up all HTTP routes
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/v1/snapshot", s.handleSnapshot)
	mux.HandleFunc("/api/v1/outages", s.handleOutages)
	mux.HandleFunc("/api/v1/providers", s.handleProviders)
	mux.HandleFunc("/api/v1/providers/", s.handleProvider)
	mux.HandleFunc("/api/v1/incidents", s.handleIncidents)
	mux.HandleFunc("/api/v1/changes", s.handleChanges)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Dashboard
	mux.Handle("/", http.HandlerFunc(s.handleDashboard))

	return mux
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
		http.Error(w, "no snapshot available", http.StatusServiceUnavailable)
		return
	}

	// ETag handling for caching
	etag := snap.etag
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")

	// GZIP compression
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		if _, err := gz.Write(snap.data); err != nil {
			http.Error(w, "failed to compress snapshot", http.StatusInternalServerError)
			return
		}
		if err := gz.Close(); err != nil {
			http.Error(w, "failed to finalize compression", http.StatusInternalServerError)
			return
		}
		return
	}

	w.Write(snap.data)
}

// handleOutages returns only non-operational providers
func (s *Server) handleOutages(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	providerList, err := s.db.GetProviders(store.ProviderFilter{
		Category:    category,
		EnabledOnly: true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter to only non-operational
	var outages []map[string]interface{}
	for _, p := range providerList {
		if p.Endpoint == "" {
			continue
		}
		
		var check store.Check
		err := s.db.QueryRow(`
			SELECT indicator, ok FROM checks 
			WHERE endpoint = ? 
			ORDER BY ts DESC LIMIT 1
		`, p.Endpoint).Scan(&check.Indicator, &check.OK)
		
		if err == nil && !check.OK {
			outages = append(outages, map[string]interface{}{
				"provider":     p.Name,
				"category":     p.Category,
				"adapter":      p.Adapter,
				"indicator":    check.Indicator,
				"endpoint":     p.Endpoint,
				"page_url":     p.PageURL,
			})
		}
	}

	json.NewEncoder(w).Encode(outages)
}

// handleProviders returns the full provider inventory
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	adapterFilter := r.URL.Query().Get("adapter")
	category := r.URL.Query().Get("category")
	enabledOnly := r.URL.Query().Get("enabled_only") == "true"

	providers, err := s.db.GetProviders(store.ProviderFilter{
		Adapter:     adapterFilter,
		Category:    category,
		EnabledOnly: enabledOnly,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		LastCheck string `json:"last_check,omitempty"`
		Notes     string `json:"notes,omitempty"`
	}

	var resp []providerResponse
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

		if p.Endpoint != "" {
			var indicator, lastCheck string
			err := s.db.QueryRow(`
				SELECT indicator, ts FROM checks 
				WHERE endpoint = ? 
				ORDER BY ts DESC LIMIT 1
			`, p.Endpoint).Scan(&indicator, &lastCheck)
			
			if err == nil {
				pr.Indicator = indicator
				pr.LastCheck = lastCheck
			}
		}

		resp = append(resp, pr)
	}

	json.NewEncoder(w).Encode(resp)
}

// handleProvider returns details for a single provider
func (s *Server) handleProvider(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/providers/")
	
	p, err := s.db.GetProvider(id)
	if err != nil {
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}

	// Get latest check
	var check store.Check
	err = s.db.QueryRow(`
		SELECT ts, http_code, latency_ms, indicator, ok, err 
		FROM checks WHERE endpoint = ? 
		ORDER BY ts DESC LIMIT 1
	`, p.Endpoint).Scan(&check.TS, &check.HTTPCode, &check.LatencyMS, &check.Indicator, &check.OK, &check.Err)

	// Get open incidents
	openIncidents, err := s.db.GetOpenIncidents()
	
	// Filter to just this provider's incidents
	var providerIncidents []store.Incident
	for _, inc := range openIncidents {
		if inc.ProviderID == p.ID {
			providerIncidents = append(providerIncidents, inc)
		}
	}

	// Get components
	var components []store.Component
	if p.Endpoint != "" {
		rows, err := s.db.Query("SELECT name, status, updated_at FROM components WHERE provider_id = ? ORDER BY name", p.ID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var c store.Component
				rows.Scan(&c.Name, &c.Status, &c.UpdatedAt)
				components = append(components, c)
			}
		}
	}

	// Get recent check history (last 24 hours)
	type checkHistory struct {
		TS       string `json:"ts"`
		Indicator string `json:"indicator"`
		LatencyMS int    `json:"latency_ms"`
		OK       bool   `json:"ok"`
	}
	
	var history []checkHistory
	if p.Endpoint != "" {
		rows, err := s.db.Query(`
			SELECT ts, indicator, latency_ms, ok FROM checks 
			WHERE endpoint = ? 
			ORDER BY ts DESC LIMIT 100
		`, p.Endpoint)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var ch checkHistory
				var okInt int
				if err := rows.Scan(&ch.TS, &ch.Indicator, &ch.LatencyMS, &okInt); err == nil {
					ch.OK = okInt == 1
					history = append(history, ch)
				}
			}
		}
	}

	response := map[string]interface{}{
		"id":          p.ID,
		"name":        p.Name,
		"category":    p.Category,
		"adapter":     p.Adapter,
		"tier":        p.Tier,
		"enabled":     p.Enabled,
		"page_url":    p.PageURL,
		"endpoint":    p.Endpoint,
		"notes":       p.Notes,
		"current":     check.Indicator,
		"ok":          check.OK,
		"last_check":  check.TS,
		"incidents":   providerIncidents,
		"components":  components,
		"check_history": history,
	}

	json.NewEncoder(w).Encode(response)
}

// handleIncidents returns incidents with search capability
func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	since := r.URL.Query().Get("since")
	impact := r.URL.Query().Get("impact")
	status := r.URL.Query().Get("status")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	var incidents []store.Incident
	var err error

	if query != "" {
		incidents, err = s.db.SearchIncidents(query, limit)
	} else {
		// Return recent incidents
		args := []interface{}{}
		where := []string{}
		
		if impact != "" {
			where = append(where, "impact = ?")
			args = append(args, impact)
		}
		if status != "" {
			where = append(where, "status = ?")
			args = append(args, status)
		}
		if since != "" {
			where = append(where, "started_at >= ?")
			args = append(args, since)
		}
		
		query := "SELECT provider_id, ext_id, title, impact, status, started_at, resolved_at, url, body, raw_json, first_seen, last_seen FROM incidents"
		if len(where) > 0 {
			query += " WHERE " + strings.Join(where, " AND ")
		}
		query += " ORDER BY started_at DESC LIMIT ?"
		args = append(args, limit)
		
		rows, qerr := s.db.Query(query, args...)
		if qerr != nil {
			http.Error(w, qerr.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		
		for rows.Next() {
			var inc store.Incident
			rows.Scan(&inc.ProviderID, &inc.ExtID, &inc.Title, &inc.Impact, &inc.Status, &inc.StartedAt, &inc.ResolvedAt, &inc.URL, &inc.Body, &inc.RawJSON, &inc.FirstSeen, &inc.LastSeen)
			incidents = append(incidents, inc)
		}
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(incidents)
}

// handleChanges returns what changed since a timestamp
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	}

	type change struct {
		Provider  string `json:"provider"`
		Indicator string `json:"indicator"`
		Old       string `json:"old_indicator"`
		New       string `json:"new_indicator"`
		Timestamp string `json:"timestamp"`
	}

	// Query checks for changes since the given timestamp
	rows, err := s.db.Query(`
		SELECT DISTINCT endpoint, indicator, ts FROM checks
		WHERE ts >= ?
		ORDER BY ts DESC
		LIMIT 100
	`, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var changes []change
	for rows.Next() {
		var c change
		var indicator string
		if err := rows.Scan(&c.Provider, &indicator, &c.Timestamp); err == nil {
			c.New = indicator
			changes = append(changes, c)
		}
	}

	if changes == nil {
		changes = []change{}
	}

	json.NewEncoder(w).Encode(changes)
}

// handleHealth returns the service's own health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Get snapshot info
	s.mu.RLock()
	snapTime := time.Time{}
	if s.snapshot != nil {
		snapTime = s.snapshot.timestamp
	}
	s.mu.RUnlock()

	staleness := time.Since(snapTime)
	
	health := map[string]interface{}{
		"status": "operational",
		"last_snapshot": snapTime.Format(time.RFC3339),
		"stale_seconds": int(staleness.Seconds()),
	}

	s.mu.RLock()
	if s.snapshot != nil {
		health["snapshot_etag"] = s.snapshot.etag
	}
	s.mu.RUnlock()

	if staleness > 30*time.Minute {
		health["status"] = "degraded"
	}

	json.NewEncoder(w).Encode(health)
}

// handleOpenAPI returns the OpenAPI specification
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, OpenAPISpec)
}

// handleMetrics returns Prometheus metrics
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "# HELP belltower_up Whether the belltower service is running\n")
	fmt.Fprintf(w, "# TYPE belltower_up gauge\n")
	fmt.Fprintf(w, "belltower_up 1\n\n")
	
	fmt.Fprintf(w, "# HELP belltower_snapshot_stale_seconds How stale the last snapshot is\n")
	fmt.Fprintf(w, "# TYPE belltower_snapshot_stale_seconds gauge\n")
	s.mu.RLock()
	if s.snapshot != nil {
		fmt.Fprintf(w, "belltower_snapshot_stale_seconds %.0f\n", time.Since(s.snapshot.timestamp).Seconds())
	} else {
		fmt.Fprintf(w, "belltower_snapshot_stale_seconds 999999\n")
	}
	s.mu.RUnlock()
	
	// Provider metrics
	providers, _ := s.db.GetProviders(store.ProviderFilter{EnabledOnly: true})
	for _, p := range providers {
		if p.Endpoint == "" {
			continue
		}
		var indicator, errMsg string
		err := s.db.QueryRow(`SELECT indicator, ok FROM checks WHERE endpoint = ? ORDER BY ts DESC LIMIT 1`, p.Endpoint).Scan(&indicator, &errMsg)
		if err == nil {
			fmt.Fprintf(w, "# HELP belltower_provider_up Whether provider %s is up\n", p.Name)
			fmt.Fprintf(w, "# TYPE belltower_provider_up gauge\n")
			isUp := indicator == "none" || indicator == "operational"
			if isUp {
				fmt.Fprintf(w, "belltower_provider_up{provider=\"%s\",category=\"%s\",adapter=\"%s\"} 1\n", p.Name, p.Category, p.Adapter)
			} else {
				fmt.Fprintf(w, "belltower_provider_up{provider=\"%s\",category=\"%s\",adapter=\"%s\"} 0\n", p.Name, p.Category, p.Adapter)
				fmt.Fprintf(w, "belltower_provider_indicator{provider=\"%s\",indicator=\"%s\"} 1\n", p.Name, indicator)
			}
		}
	}
}

// handleDashboard serves the HTML dashboard
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	// Serve static files
	if r.URL.Path == "/static/htmx.min.js" {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(htmxJS)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	tmpl := template.Must(template.New("dashboard").Parse(dashboardTemplate))
	tmpl.Execute(w, nil)
}

// RefreshSnapshot rebuilds the in-memory snapshot cache
func (s *Server) RefreshSnapshot(ctx context.Context) error {
	s.refreshSnapshot()
	return nil
}

func (s *Server) refreshSnapshot() {
	snapshot, err := s.db.BuildSnapshot()
	if err != nil {
		return
	}

	jsonData, err := snapshot.Marshal()
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = &snapshotCache{
		data:      jsonData,
		etag:      snapshot.ETag(),
		timestamp: time.Now(),
	}
}

// OpenAPISpec is the embedded OpenAPI specification
const OpenAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Belltower Status Page Monitor API",
    "version": "1.0.0",
    "description": "API for monitoring vendor status pages"
  },
  "paths": {
    "/api/v1/snapshot": {
      "get": {
        "summary": "Get full current state snapshot",
        "responses": {
          "200": {
            "description": "Current snapshot",
            "content": {
              "application/json": {}
            }
          },
          "304": {
            "description": "Not modified (ETag match)"
          }
        }
      }
    },
    "/api/v1/outages": {
      "get": {
        "summary": "Get current outages",
        "parameters": [
          {"name": "category", "in": "query", "schema": {"type": "string"}},
          {"name": "min_impact", "in": "query", "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {
            "description": "List of outages",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    },
    "/api/v1/providers": {
      "get": {
        "summary": "Get provider inventory",
        "parameters": [
          {"name": "adapter", "in": "query", "schema": {"type": "string"}},
          {"name": "category", "in": "query", "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {
            "description": "Provider list",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    },
    "/api/v1/providers/{id}": {
      "get": {
        "summary": "Get provider details including incidents and components",
        "responses": {
          "200": {
            "description": "Provider details",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    },
    "/api/v1/incidents": {
      "get": {
        "summary": "Search incidents",
        "parameters": [
          {"name": "q", "in": "query", "schema": {"type": "string"}},
          {"name": "since", "in": "query", "schema": {"type": "string", "format": "date-time"}},
          {"name": "impact", "in": "query", "schema": {"type": "string"}},
          {"name": "status", "in": "query", "schema": {"type": "string"}},
          {"name": "limit", "in": "query", "schema": {"type": "integer"}}
        ],
        "responses": {
          "200": {
            "description": "Incident list",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    },
    "/api/v1/changes": {
      "get": {
        "summary": "Get recent changes since a timestamp",
        "parameters": [
          {"name": "since", "in": "query", "schema": {"type": "string", "format": "date-time"}}
        ],
        "responses": {
          "200": {
            "description": "Change list",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    },
    "/api/v1/health": {
      "get": {
        "summary": "Get service health",
        "responses": {
          "200": {
            "description": "Health status",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    },
    "/metrics": {
      "get": {
        "summary": "Prometheus metrics",
        "responses": {
          "200": {
            "description": "Metrics in Prometheus format",
            "content": {
              "text/plain": {}
            }
          }
        }
      }
    }
  }
}`

//go:embed static/htmx.min.js
var htmxJS []byte

//go:embed templates/dashboard.html
var dashboardTemplate string
