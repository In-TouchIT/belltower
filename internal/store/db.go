package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// Schema is the embedded SQL schema
func Schema() (io.Reader, error) {
	return schemaFS.Open("schema.sql")
}

// Provider represents a service whose status we monitor
type Provider struct {
	ID       string `json:"id" db:"id"`
	Name     string `json:"name" db:"name"`
	Category string `json:"category" db:"category"`
	PageURL  string `json:"page_url" db:"page_url"`
	Adapter  string `json:"adapter" db:"adapter"`
	Endpoint string `json:"endpoint" db:"endpoint"`
	Tier     int    `json:"tier" db:"tier"`
	Enabled  bool   `json:"enabled" db:"enabled"`
	Notes    string `json:"notes,omitempty" db:"notes"`
}

// Check represents a single polling result
type Check struct {
	Endpoint  string `json:"endpoint" db:"endpoint"`
	TS        string `json:"ts" db:"ts"`
	HTTPCode  int    `json:"http_code" db:"http_code"`
	LatencyMS int    `json:"latency_ms" db:"latency_ms"`
	Indicator string `json:"indicator" db:"indicator"`
	OK        bool   `json:"ok" db:"ok"`
	Err       string `json:"err,omitempty" db:"err"`
}

// Incident represents a service incident
type Incident struct {
	ProviderID string `json:"provider_id" db:"provider_id"`
	ExtID      string `json:"ext_id" db:"ext_id"`
	Title      string `json:"title" db:"title"`
	Impact     string `json:"impact" db:"impact"`
	Status     string `json:"status" db:"status"`
	StartedAt  string `json:"started_at" db:"started_at"`
	ResolvedAt string `json:"resolved_at,omitempty" db:"resolved_at"`
	URL        string `json:"url,omitempty" db:"url"`
	Body       string `json:"body,omitempty" db:"body"`
	RawJSON    string `json:"raw_json,omitempty" db:"raw_json"`
	FirstSeen  string `json:"first_seen" db:"first_seen"`
	LastSeen   string `json:"last_seen" db:"last_seen"`
}

// Component represents a sub-component of a provider's service
type Component struct {
	ProviderID string `json:"provider_id" db:"provider_id"`
	Name       string `json:"name" db:"name"`
	Status     string `json:"status" db:"status"`
	UpdatedAt  string `json:"updated_at" db:"updated_at"`
}

// Snapshot represents the precomputed state at a point in time
type Snapshot struct {
	CycleID int64  `json:"cycle_id" db:"cycle_id"`
	BuiltAt string `json:"built_at" db:"built_at"`
	JSON    []byte `json:"-" db:"json"`
	ETag    string `json:"etag" db:"etag"`
}

// DB wraps a sql.DB with convenient methods
type DB struct {
	*sql.DB
}

// Open opens a SQLite database with WAL mode and appropriate settings
func Open(dsn string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Apply SQLite pragmas
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return nil, fmt.Errorf("failed to set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		return nil, fmt.Errorf("failed to set synchronous: %w", err)
	}

	d := &DB{db}

	// Run schema if it hasn't been initialized
	if err := d.InitSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return d, nil
}

// InitSchema applies the embedded schema to the database
func (d *DB) InitSchema() error {
	reader, err := Schema()
	if err != nil {
		return fmt.Errorf("failed to read schema: %w", err)
	}

	schema, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read schema content: %w", err)
	}

	_, err = d.Exec(string(schema))
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	return nil
}

// Close closes the database connection
func (d *DB) Close() error {
	return d.DB.Close()
}
