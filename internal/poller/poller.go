package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ccarson/belltower/internal/adapters"
	"github.com/ccarson/belltower/internal/store"
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
	
	mu         sync.Mutex
	lastRun    time.Time
	lastError  error
	runCount   int64
}

// Config holds poller configuration
type Config struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	UserAgent   string
}

// New creates a new Poller
func New(db *store.DB, registry *adapters.Registry, cfg Config) *Poller {
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
	p.mu.Unlock()

	// Get all enabled providers
	providers, err := p.db.GetProviders(store.ProviderFilter{EnabledOnly: true})
	if err != nil {
		return fmt.Errorf("failed to get providers: %w", err)
	}

	if len(providers) == 0 {
		fmt.Println("No providers to poll")
		return nil
	}

	// Deduplicate by endpoint
	seenEndpoints := make(map[string]bool)
	var uniqueProviders []store.Provider
	for _, prov := range providers {
		if prov.Endpoint == "" {
			continue
		}
		if seenEndpoints[prov.Endpoint] {
			continue
		}
		seenEndpoints[prov.Endpoint] = true
		uniqueProviders = append(uniqueProviders, prov)
	}

	fmt.Printf("Polling %d unique endpoints (from %d enabled providers)\n", len(uniqueProviders), len(providers))

	// Use worker pool for concurrent polling
	var wg sync.WaitGroup
	jobs := make(chan store.Provider, p.concurrency*2)
	errors := make(chan error, len(uniqueProviders))

	// Start workers
	for i := 0; i < p.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for provider := range jobs {
				p.pollProvider(ctx, provider)
			}
		}()
	}

	// Send jobs
	for _, prov := range uniqueProviders {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- prov:
		}
	}
	close(jobs)

	// Wait for completion
	wg.Wait()
	close(errors)

	// Report errors
	errorCount := 0
	for err := range errors {
		if err != nil {
			errorCount++
		}
	}

	if errorCount > 0 {
		fmt.Printf("Polling cycle completed with %d errors\n", errorCount)
	}

	// Build and save snapshot
	if err := p.buildSnapshot(); err != nil {
		return fmt.Errorf("failed to build snapshot: %w", err)
	}

	// Prune old data periodically
	if p.runCount%144 == 0 { // Every 24 hours with 10min intervals
		p.prune()
	}

	return nil
}

// pollProvider fetches status for a single provider
func (p *Poller) pollProvider(ctx context.Context, provider store.Provider) {
	// Skip manual adapters
	if provider.Adapter == "manual" || provider.Endpoint == "" {
		return
	}

	// Add jitter to prevent thundering herd (5-15% randomization)
	jitter := time.Duration(1) * time.Second
	time.Sleep(jitter)

	providerInfo := adapters.ProviderInfo{
		ID:       provider.ID,
		Name:     provider.Name,
		Category: provider.Category,
		PageURL:  provider.PageURL,
		Endpoint: provider.Endpoint,
		Adapter:  provider.Adapter,
	}

	start := time.Now()
	result, err := p.registry.Fetch(ctx, providerInfo)
	latency := time.Since(start)

	check := store.Check{
		Endpoint:  provider.Endpoint,
		TS:        time.Now().UTC().Format(time.RFC3339),
		HTTPCode:  200, // We'd capture this in the actual adapter
		LatencyMS: int(latency.Milliseconds()),
		OK:        err == nil,
	}

	if err != nil {
		check.Indicator = "unknown"
		check.Err = err.Error()
	} else {
		check.Indicator = string(result.Indicator)
	}

	// Save check result
	if dbErr := p.db.UpsertCheck(check); dbErr != nil {
		fmt.Printf("Failed to save check for %s: %v\n", provider.Name, dbErr)
	}

	// Save incidents
	if err == nil {
		for _, inc := range result.Incidents {
			incident := store.Incident{
				ProviderID: provider.ID,
				ExtID:      inc.ExtID,
				Title:      inc.Title,
				Impact:     inc.Impact,
				Status:     inc.Status,
				StartedAt:  inc.StartedAt.Format(time.RFC3339),
				ResolvedAt: inc.ResolvedAt.Format(time.RFC3339),
				URL:        inc.URL,
				Body:       inc.Body,
				RawJSON:    inc.RawJSON,
			}
			if saveErr := p.db.UpsertIncident(incident); saveErr != nil {
				fmt.Printf("Failed to save incident for %s: %v\n", provider.Name, saveErr)
			}
		}

		// Save components
		for _, comp := range result.Components {
			component := store.Component{
				ProviderID: provider.ID,
				Name:       comp.Name,
				Status:     comp.Status,
				UpdatedAt:  comp.UpdatedAt.Format(time.RFC3339),
			}
			if saveErr := p.db.UpsertComponent(component); saveErr != nil {
				fmt.Printf("Failed to save component for %s: %v\n", provider.Name, saveErr)
			}
		}
	}
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

	fmt.Printf("Snapshot saved: %d providers, ETag: %s\n", len(snapshot.Providers), etag)

	// Notify API server to refresh its cache
	if p.SnapshotRefresh != nil {
		if err := p.SnapshotRefresh(); err != nil {
			fmt.Printf("Warning: snapshot refresh callback failed: %v\n", err)
		}
	}

	return nil
}

// prune removes old data
func (p *Poller) prune() {
	pruner := store.NewPruner(p.db)
	if count, err := pruner.PruneChecks(90 * 24 * time.Hour); err != nil {
		fmt.Printf("Warning: failed to prune checks: %v\n", err)
	} else if count > 0 {
		fmt.Printf("Pruned %d check records older than 90 days\n", count)
	}

	if count, err := pruner.PruneSnapshots(50); err != nil {
		fmt.Printf("Warning: failed to prune snapshots: %v\n", err)
	} else if count > 0 {
		fmt.Printf("Pruned %d snapshots older than 50 cycles\n", count)
	}
}

// Run continuously polls at the configured interval
func (p *Poller) Run(ctx context.Context) {
	// Run once immediately
	if err := p.RunOnce(ctx); err != nil {
		fmt.Printf("Initial poll failed: %v\n", err)
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.RunOnce(ctx); err != nil {
				fmt.Printf("Poll cycle failed: %v\n", err)
			}
		}
	}
}

// GetStats returns poller statistics
func (p *Poller) GetStats() PollerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PollerStats{
		LastRun:    p.lastRun,
		RunCount:   p.runCount,
		Interval:   p.interval,
		Concurrency: p.concurrency,
	}
}

// PollerStats contains poller statistics
type PollerStats struct {
	LastRun     time.Time
	RunCount    int64
	Interval    time.Duration
	Concurrency int
}
