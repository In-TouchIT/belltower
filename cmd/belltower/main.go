package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
It polls 218 unique endpoints every 10 minutes and provides both a web dashboard
and an API for integration with other tools.`,
	Run: runServer,
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

	viper.BindPFlags(rootCmd.Flags())
}

func initConfig() {
	viper.SetEnvPrefix("BELLTOWER")
	viper.AutomaticEnv()
}

func runServer(cmd *cobra.Command, args []string) {
	dbPath := viper.GetString("db-path")
	providersFile := viper.GetString("providers-file")
	addr := viper.GetString("addr")
	pollInterval := viper.GetDuration("poll-interval")
	pollTimeout := viper.GetDuration("poll-timeout")
	concurrency := viper.GetInt("concurrency")
	userAgent := viper.GetString("user-agent")

	// Ensure data directory exists
	dataDir := "./data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Open database
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=normal", dbPath)
	db, err := store.Open(dsn)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	fmt.Printf("Database opened at: %s\n", dbPath)

	// Load providers from YAML and populate database
	providerInfos, err := adapters.LoadProvidersFromYAML(providersFile)
	if err != nil {
		log.Fatalf("Failed to load providers: %v", err)
	}
	fmt.Printf("Loaded %d providers from %s\n", len(providerInfos), providersFile)

	// Upsert providers into database
	for _, p := range providerInfos {
		provider := store.Provider{
			ID:       p.ID,
			Name:     p.Name,
			Category: p.Category,
			PageURL:  p.PageURL,
			Adapter:  p.Adapter,
			Endpoint: p.Endpoint,
			Enabled:  true,
		}
		if err := db.UpsertProvider(provider); err != nil {
			log.Printf("Warning: failed to upsert provider %s: %v", p.ID, err)
		}
	}

	// Initialize adapter registry
	registry := adapters.NewRegistry(userAgent, pollTimeout)
	fmt.Printf("Registered adapters: %v\n", registry.ListAdapters())

	// Initialize API server first (needed for snapshot refresh callback)
	serverCfg := api.Config{
		Addr:      addr,
		UserAgent: userAgent,
	}
	server := api.NewServer(db, registry, serverCfg)

	// Initialize poller
	pollerCfg := poller.Config{
		Interval:    pollInterval,
		Timeout:     pollTimeout,
		Concurrency: concurrency,
		UserAgent:   userAgent,
	}
	pollr := poller.New(db, registry, pollerCfg)

	// Wire up snapshot refresh callback
	pollr.SnapshotRefresh = func() error {
		return server.RefreshSnapshot(context.Background())
	}

	// Start poller in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pollr.Run(ctx)

	// Start HTTP server
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server.Routes(),
	}

	// Graceful shutdown
	go func() {
		fmt.Printf("Belltower listening on %s\n", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\nShutting down...")

	// Shutdown gracefully
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	fmt.Println("Belltower stopped")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
