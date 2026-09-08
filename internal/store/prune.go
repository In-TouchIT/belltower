package store

import (
	"time"
)

// Pruner handles cleanup of old data
type Pruner struct {
	db *DB
}

// NewPruner creates a new Pruner
func NewPruner(db *DB) *Pruner {
	return &Pruner{db: db}
}

// PruneChecks removes check records older than maxAge
func (p *Pruner) PruneChecks(maxAge time.Duration) (int64, error) {
	return p.db.PruneOldChecks(maxAge)
}

// PruneSnapshots keeps only the last n snapshots
func (p *Pruner) PruneSnapshots(keep int) (int64, error) {
	return p.db.PruneOldSnapshots(keep)
}

// PruneResolvedIncidents removes resolved incidents last seen before maxAge ago.
func (p *Pruner) PruneResolvedIncidents(maxAge time.Duration) (int64, error) {
	return p.db.PruneResolvedIncidents(maxAge)
}
