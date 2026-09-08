package poller

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/In-TouchIT/belltower/internal/adapters"
	"github.com/In-TouchIT/belltower/internal/store"
)

// Poller periodically polls provider status pages
type Poller struct {
	db          *store.DB
	registry    *adapters.Registry
	interval    time.Duration
	timeout     time.Duration
	concurrency int
	userAgent   string

	// SnapshotRefresh is called after each polling cycle to update the API cache
	SnapshotRefresh func() error

	mu       sync.Mutex
	lastRun  time.Time
	runCount int64
	lastErrs int
}

// Config holds poller configuration
type Config struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	UserAgent   string
}

// Stats is a point-in-time view of poller activity.
type Stats struct {
	LastRun     time.Time
	RunCount    int64
	LastErrors  int
	Interval    time.Duration
	Concurrency int
}

// New creates a new Poller
func New(db *store.DB, registry *adapters.Registry, cfg Config) *Poller {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	return &Poller{
		db:          db,
		registry:    registry,
		interval:    cfg.Interval,
		timeout:     cfg.Timeout,
		concurrency: cfg.Concurrency,
		userAgent:   cfg.UserAgent,
	}
}

// RunOnce executes a single polling cycle
func (p *Poller) RunOnce(ctx context.Context) error {
	p.mu.Lock()
	p.lastRun = time.Now()
	p.runCount++
	runCount := p.runCount
	p.mu.Unlock()

	providers, err := p.db.GetProviders(store.ProviderFilter{EnabledOnly: true})
	if err != nil {
		return fmt.Errorf("failed to get providers: %w", err)
	}

	// One HTTP request per distinct endpoint, but every provider sharing that
	// endpoint still gets its incidents and components recorded.
	byEndpoint := make(map[string][]store.Provider)
	var endpoints []string
	for _, prov := range providers {
		if prov.Endpoint == "" || prov.Adapter == "manual" {
			continue
		}
		if _, seen := byEndpoint[prov.Endpoint]; !seen {
			endpoints = append(endpoints, prov.Endpoint)
		}
		byEndpoint[prov.Endpoint] = append(byEndpoint[prov.Endpoint], prov)
	}

	if len(endpoints) == 0 {
		log.Println("No providers to poll")
		return nil
	}

	log.Printf("Polling %d unique endpoints (from %d enabled providers)", len(endpoints), len(providers))

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		errCount int
	)
	jobs := make(chan string)

	for i := 0; i < p.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for endpoint := range jobs {
				if ctx.Err() != nil {
					return
				}
				if failed := p.pollEndpoint(ctx, byEndpoint[endpoint]); failed {
					errMu.Lock()
					errCount++
					errMu.Unlock()
				}
			}
		}()
	}

	for _, endpoint := range endpoints {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- endpoint:
		}
	}
	close(jobs)
	wg.Wait()

	p.mu.Lock()
	p.lastErrs = errCount
	p.mu.Unlock()

	if errCount > 0 {
		log.Printf("Polling cycle completed with %d/%d endpoints failing", errCount, len(endpoints))
	}

	if err := p.buildSnapshot(); err != nil {
		return fmt.Errorf("failed to build snapshot: %w", err)
	}

	// Prune roughly daily, independent of the configured interval.
	if pruneEvery := pruneCycles(p.interval); runCount%pruneEvery == 0 {
		p.prune()
	}

	return nil
}

// pruneCycles is how many cycles fit in a day, floored at 1.
func pruneCycles(interval time.Duration) int64 {
	if interval <= 0 {
		return 1
	}
	n := int64(24 * time.Hour / interval)
	if n < 1 {
		return 1
	}
	return n
}

// pollEndpoint fetches one endpoint and records the result against every
// provider that shares it. It reports whether the fetch failed.
func (p *Poller) pollEndpoint(ctx context.Context, providers []store.Provider) bool {
	if len(providers) == 0 {
		return false
	}
	primary := providers[0]

	// Small random stagger so 20 workers do not fire in lockstep at the top
	// of every cycle. Unlike a fixed sleep, this actually decorrelates.
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	select {
	case <-ctx.Done():
		return false
	case <-time.After(jitter):
	}

	providerInfo := adapters.ProviderInfo{
		ID:       primary.ID,
		Name:     primary.Name,
		Category: primary.Category,
		PageURL:  primary.PageURL,
		Endpoint: primary.Endpoint,
		Adapter:  primary.Adapter,
		Tier:     primary.Tier,
	}

	start := time.Now()
	result, err := p.registry.Fetch(ctx, providerInfo)
	latency := time.Since(start)

	check := store.Check{
		Endpoint:  primary.Endpoint,
		TS:        time.Now().UTC().Format(time.RFC3339),
		HTTPCode:  result.HTTPStatus, // 0 when the request never got a response
		LatencyMS: int(latency.Milliseconds()),
		OK:        err == nil,
	}

	if err != nil {
		// A failed poll is recorded as unknown, never as operational: we do
		// not know the provider's state, and saying "none" here would report
		// a green dashboard for every vendor we cannot reach.
		check.Indicator = string(adapters.IndicatorUnknown)
		check.Err = err.Error()
		if primary.Tier <= 2 {
			log.Printf("[WARN] Failed polling Tier %d provider %s (%s): %v",
				primary.Tier, primary.Name, primary.Endpoint, err)
		}
	} else {
		check.Indicator = string(result.Indicator)
	}

	if dbErr := p.db.UpsertCheck(check); dbErr != nil {
		log.Printf("Failed to save check for %s: %v", primary.Name, dbErr)
	}

	if err != nil {
		return true
	}

	for _, provider := range providers {
		p.saveResult(provider, result)
	}
	return false
}

// saveResult persists the incidents and components from a successful fetch.
func (p *Poller) saveResult(provider store.Provider, result adapters.Result) {
	for _, inc := range result.Incidents {
		if inc.ExtID == "" {
			continue // no stable key, would collide with every other such incident
		}
		if dbErr := p.db.UpsertIncident(store.Incident{
			ProviderID: provider.ID,
			ExtID:      inc.ExtID,
			Title:      inc.Title,
			Impact:     inc.Impact,
			Status:     inc.Status,
			StartedAt:  formatTime(inc.StartedAt),
			ResolvedAt: formatTime(inc.ResolvedAt),
			URL:        inc.URL,
			Body:       inc.Body,
			RawJSON:    inc.RawJSON,
		}); dbErr != nil {
			log.Printf("Failed to save incident for %s: %v", provider.Name, dbErr)
		}
	}

	for _, comp := range result.Components {
		if comp.Name == "" {
			continue
		}
		if dbErr := p.db.UpsertComponent(store.Component{
			ProviderID: provider.ID,
			Name:       comp.Name,
			Status:     comp.Status,
			UpdatedAt:  formatTime(comp.UpdatedAt),
		}); dbErr != nil {
			log.Printf("Failed to save component for %s: %v", provider.Name, dbErr)
		}
	}
}

// formatTime renders a timestamp for storage, mapping the zero time to the
// empty string so "unknown" is stored as NULL rather than as year 1.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// buildSnapshot creates and saves a snapshot of current state
func (p *Poller) buildSnapshot() error {
	snapshot, err := p.db.BuildSnapshot()
	if err != nil {
		return fmt.Errorf("failed to build snapshot: %w", err)
	}

	jsonData, err := snapshot.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %w", err)
	}

	etag := snapshot.ETag()
	builtAt := time.Now().UTC().Format(time.RFC3339)

	if _, err := p.db.SaveSnapshot(builtAt, jsonData, etag); err != nil {
		return fmt.Errorf("failed to save snapshot: %w", err)
	}

	log.Printf("Snapshot saved: %d providers, %d open incidents, ETag: %s",
		len(snapshot.Providers), snapshot.Stats.OpenIncidents, etag)

	if p.SnapshotRefresh != nil {
		if err := p.SnapshotRefresh(); err != nil {
			log.Printf("Warning: snapshot refresh callback failed: %v", err)
		}
	}

	return nil
}

// prune removes old data
func (p *Poller) prune() {
	pruner := store.NewPruner(p.db)
	if count, err := pruner.PruneChecks(90 * 24 * time.Hour); err != nil {
		log.Printf("Warning: failed to prune checks: %v", err)
	} else if count > 0 {
		log.Printf("Pruned %d check records older than 90 days", count)
	}

	if count, err := pruner.PruneSnapshots(50); err != nil {
		log.Printf("Warning: failed to prune snapshots: %v", err)
	} else if count > 0 {
		log.Printf("Pruned %d old snapshots", count)
	}

	if count, err := pruner.PruneResolvedIncidents(90 * 24 * time.Hour); err != nil {
		log.Printf("Warning: failed to prune incidents: %v", err)
	} else if count > 0 {
		log.Printf("Pruned %d resolved incidents older than 90 days", count)
	}
}

// Run continuously polls at the configured interval
func (p *Poller) Run(ctx context.Context) {
	if err := p.RunOnce(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Initial poll failed: %v", err)
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.RunOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("Poll cycle failed: %v", err)
			}
		}
	}
}

// GetStats returns poller statistics
func (p *Poller) GetStats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{
		LastRun:     p.lastRun,
		RunCount:    p.runCount,
		LastErrors:  p.lastErrs,
		Interval:    p.interval,
		Concurrency: p.concurrency,
	}
}
