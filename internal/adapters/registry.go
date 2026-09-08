package adapters

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Registry holds all available adapters.
//
// The adapter map is populated once in NewRegistry and only read afterwards,
// so it needs no lock.
type Registry struct {
	adapters   map[string]Adapter
	httpClient *http.Client
	userAgent  string
	timeout    time.Duration
}

// NewRegistry creates a new adapter registry. timeout bounds each individual
// upstream request.
func NewRegistry(userAgent string, timeout time.Duration) *Registry {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			// net/http drops most headers when the redirect crosses hosts;
			// re-apply the identifying one so we stay a good citizen.
			req.Header.Set("User-Agent", userAgent)
			return nil
		},
	}

	r := &Registry{
		adapters:   make(map[string]Adapter),
		httpClient: client,
		userAgent:  userAgent,
		timeout:    timeout,
	}

	// Generic status-page platforms
	r.Register("statuspage", NewStatusPageAdapter(client, userAgent))
	r.Register("statusio", NewStatusIOAdapter(client, userAgent))
	r.Register("betterstack", NewBetterStackAdapter(client, userAgent))
	r.Register("instatus", NewInstatusAdapter(client, userAgent))
	r.Register("sorryapp", NewSorryAppAdapter(client, userAgent))
	r.Register("rss", NewRSSAdapter(client, userAgent))

	// Vendor-specific APIs
	r.Register("gcp", NewGCPAdapter(client, userAgent))
	r.Register("aws", NewAWSAdapter(client, userAgent))
	r.Register("slack", NewSlackAdapter(client, userAgent))
	r.Register("heroku", NewHerokuAdapter(client, userAgent))
	r.Register("gworkspace", NewGWorkspaceAdapter(client, userAgent))
	r.Register("salesforce", NewSalesforceAdapter(client, userAgent))
	r.Register("apple", NewAppleAdapter(client, userAgent))
	r.Register("webex", NewWebexAdapter(client, userAgent))

	return r
}

// Register adds a new adapter to the registry.
func (r *Registry) Register(name string, adapter Adapter) {
	r.adapters[name] = adapter
}

// Get returns an adapter by name.
func (r *Registry) Get(name string) (Adapter, error) {
	adapter, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("adapter '%s' not found", name)
	}
	return adapter, nil
}

// Fetch calls the appropriate adapter for a provider.
//
// The per-request deadline comes from the configured poll timeout; the retrying
// adapters get a little more headroom so a retry budget is not cut off midway.
func (r *Registry) Fetch(ctx context.Context, p ProviderInfo) (Result, error) {
	adapter, ok := r.adapters[p.Adapter]
	if !ok {
		return Result{}, fmt.Errorf("unknown adapter '%s' for provider %s", p.Adapter, p.Name)
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout*3)
	defer cancel()

	return adapter.Fetch(ctx, p)
}

// ListAdapters returns the names of all registered adapters, sorted.
func (r *Registry) ListAdapters() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
