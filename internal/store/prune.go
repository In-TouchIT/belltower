package store

import (
	"fmt"
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
	count, err := p.db.PruneOldChecks(maxAge)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		fmt.Printf("Pruned %d old check records\n", count)
	}
	return count, nil
}

// PruneSnapshots keeps only the last n snapshots
func (p *Pruner) PruneSnapshots(keep int) (int64, error) {
	count, err := p.db.PruneOldSnapshots(keep)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		fmt.Printf("Pruned %d old snapshots\n", count)
	}
	return count, nil
}
