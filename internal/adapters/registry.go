package adapters

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Registry holds all available adapters
type Registry struct {
	adapters    map[string]Adapter
	httpClient  *http.Client
	userAgent   string

	mu          sync.RWMutex
	lastStats   PollerStats
}

// PollerStats holds polling statistics
type PollerStats struct {
	LastRun      time.Time
	RunCount     int64
	AdapterErrors map[string]int
}

// NewRegistry creates a new adapter registry
func NewRegistry(userAgent string, timeout time.Duration) *Registry {
	client := &http.Client{
		Timeout: timeout,
	}

	r := &Registry{
		adapters:  make(map[string]Adapter),
		httpClient: client,
		userAgent:  userAgent,
	}

	// Register all adapters
	r.Register("statuspage", NewStatusPageAdapter(client, userAgent))
	r.Register("statusio", NewStatusIOAdapter(client, userAgent))
	r.Register("betterstack", NewBetterStackAdapter(client, userAgent))
	r.Register("instatus", NewInstatusAdapter(client, userAgent))
	r.Register("sorryapp", NewSorryAppAdapter(client, userAgent))
	
	// Hand-rolled adapters
	r.Register("gcp", NewGCPAdapter(client, userAgent))
	r.Register("aws", NewAWSAdapter(client, userAgent))
	// r.Register("rss", NewRSSAdapter(client, userAgent)) // Azure RSS
	
	// These use statuspage.io API pattern but have custom endpoints
	r.Register("slack", NewSlackAdapter(client, userAgent))
	r.Register("heroku", NewHerokuAdapter(client, userAgent))
	r.Register("gworkspace", NewGWorkspaceAdapter(client, userAgent))
	r.Register("salesforce", NewSalesforceAdapter(client, userAgent))
	r.Register("rss", NewRSSAdapter(client, userAgent))

	return r
}

// Register adds a new adapter to the registry
func (r *Registry) Register(name string, adapter Adapter) {
	r.adapters[name] = adapter
}

// Get returns an adapter by name
func (r *Registry) Get(name string) (Adapter, error) {
	adapter, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("adapter '%s' not found", name)
	}
	return adapter, nil
}

// Fetch calls the appropriate adapter for a provider
func (r *Registry) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	adapter, ok := r.adapters[p.Adapter]
	if !ok {
		return Result{}, fmt.Errorf("unknown adapter '%s' for provider %s", p.Adapter, p.Name)
	}

	// Add jitter to prevent thundering herd
	time.Sleep(time.Duration(1) * time.Second)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return adapter.Fetch(ctx, p)
}

// ListAdapters returns the names of all registered adapters
func (r *Registry) ListAdapters() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}
