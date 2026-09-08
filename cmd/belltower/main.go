package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ccarson/belltower/internal/adapters"
	"github.com/ccarson/belltower/internal/api"
	"github.com/ccarson/belltower/internal/poller"
	"github.com/ccarson/belltower/internal/store"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:   "belltower",
	Short: "Belltower - Status Page Monitor",
	Long: `Belltower aggregates vendor status pages into a single dashboard and API.
It polls vendor status endpoints on an interval and provides both a web
dashboard and an API for integration with other tools.`,
	RunE: runServer,
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.Flags().String("db-path", "./data/belltower.db", "Path to SQLite database")
	rootCmd.Flags().String("providers-file", "./providers.yaml", "Path to providers configuration")
	rootCmd.Flags().String("addr", ":8088", "Address to listen on")
	rootCmd.Flags().Duration("poll-interval", 10*time.Minute, "Polling interval")
	rootCmd.Flags().Duration("poll-timeout", 10*time.Second, "Per-request timeout")
	rootCmd.Flags().Int("concurrency", 20, "Number of concurrent polling workers")
	rootCmd.Flags().String("user-agent", "belltower/1.0 (status monitoring; contact@intouchit.com)", "User-Agent header for outgoing requests")

	if err := viper.BindPFlags(rootCmd.Flags()); err != nil {
		log.Fatalf("Failed to bind flags: %v", err)
	}
}

func initConfig() {
	viper.SetEnvPrefix("BELLTOWER")
	// Flag names use dashes; environment variables use underscores. Without
	// this replacer viper looks up BELLTOWER_POLL-INTERVAL, which nothing can
	// set, so every hyphenated env var was silently ignored.
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
}

// healthCmd lets the container health check run without curl, which does not
// exist in the scratch image the Dockerfile builds.
var healthCmd = &cobra.Command{
	Use:   "healthcheck",
	Short: "Probe the local /api/v1/health endpoint and exit non-zero if unhealthy",
	RunE: func(cmd *cobra.Command, args []string) error {
		// The root command's --addr is a local flag, so it is not inherited
		// here; healthcheck declares its own and falls back to the shared
		// viper value (and thus BELLTOWER_ADDR) when it is not given.
		addr, err := cmd.Flags().GetString("addr")
		if err != nil {
			return err
		}
		if addr == "" {
			addr = viper.GetString("addr")
		}
		if addr == "" {
			addr = ":8088"
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + healthTarget(addr) + "/api/v1/health")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("health check returned %d", resp.StatusCode)
		}
		return nil
	},
}

// healthTarget turns a listen address into something dialable. A server bound
// to ":8088" or "0.0.0.0:8088" is reached over the loopback interface.
func healthTarget(addr string) string {
	host, port, found := strings.Cut(addr, ":")
	if !found {
		return net.JoinHostPort(addr, "8088")
	}
	if host == "" || host == "0.0.0.0" || host == "[::]" || host == "::" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "8088"
	}
	return net.JoinHostPort(host, port)
}

func runServer(cmd *cobra.Command, args []string) error {
	dbPath := viper.GetString("db-path")
	providersFile := viper.GetString("providers-file")
	addr := viper.GetString("addr")
	pollInterval := viper.GetDuration("poll-interval")
	pollTimeout := viper.GetDuration("poll-timeout")
	concurrency := viper.GetInt("concurrency")
	userAgent := viper.GetString("user-agent")

	// Create the directory the database actually lives in, not a hardcoded
	// ./data that may have nothing to do with --db-path.
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create database directory %s: %w", dir, err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=normal", dbPath)
	db, err := store.Open(dsn)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()
	log.Printf("Database opened at: %s", dbPath)

	providerConfigs, err := adapters.LoadProvidersFromYAML(providersFile)
	if err != nil {
		return fmt.Errorf("failed to load providers: %w", err)
	}

	registry := adapters.NewRegistry(userAgent, pollTimeout)
	known := make(map[string]bool)
	for _, name := range registry.ListAdapters() {
		known[name] = true
	}

	enabled := 0
	for _, p := range providerConfigs {
		// A provider referencing an adapter we do not implement is recorded
		// but never polled, rather than failing every cycle.
		isEnabled := p.Enabled
		if isEnabled && p.Adapter != "manual" && !known[p.Adapter] {
			log.Printf("Warning: provider %s references unknown adapter %q; disabling", p.ID, p.Adapter)
			isEnabled = false
		}
		if isEnabled {
			enabled++
		}

		if saveErr := db.UpsertProvider(store.Provider{
			ID:       p.ID,
			Name:     p.Name,
			Category: p.Category,
			PageURL:  p.PageURL,
			Adapter:  p.Adapter,
			Endpoint: p.Endpoint,
			Tier:     p.Tier,
			Enabled:  isEnabled,
			Notes:    p.Notes,
		}); saveErr != nil {
			log.Printf("Warning: failed to upsert provider %s: %v", p.ID, saveErr)
		}
	}
	log.Printf("Loaded %d providers from %s (%d enabled)", len(providerConfigs), providersFile, enabled)
	log.Printf("Registered adapters: %v", registry.ListAdapters())

	server := api.NewServer(db, api.Config{Addr: addr})

	pollr := poller.New(db, registry, poller.Config{
		Interval:    pollInterval,
		Timeout:     pollTimeout,
		Concurrency: concurrency,
		UserAgent:   userAgent,
	})
	pollr.SnapshotRefresh = func() error {
		return server.RefreshSnapshot(context.Background())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pollr.Run(ctx)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: server.Routes(),
		// Without these a single slow client can hold a connection open
		// indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Belltower listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case <-quit:
		log.Println("Shutting down...")
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Belltower stopped")
	return nil
}

func main() {
	healthCmd.Flags().String("addr", "", "Address of the running server (defaults to BELLTOWER_ADDR, else :8088)")
	rootCmd.AddCommand(healthCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
