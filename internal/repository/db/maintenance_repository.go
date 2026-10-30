package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// MaintenanceRepository holds housekeeping queries for tables that have a
// retention policy.
//
// It is intentionally separate from repository.WorkspaceRepository: the
// request-serving sync repository must not grow a prune method, and only
// cmd/prune needs this type.
//
// Scope note: entity_changes is never pruned here. Clients pull with a seq
// cursor and may resume from an arbitrarily old cursor, so deleting pull-log
// rows would make deltas disappear silently. Only entity_ops (the idempotency
// log) is retained for a bounded time.
type MaintenanceRepository struct {
	db *gorm.DB
}

// NewMaintenanceRepository returns a Postgres-backed maintenance repository.
func NewMaintenanceRepository(db *gorm.DB) *MaintenanceRepository {
	return &MaintenanceRepository{db: db}
}

// CountOpsBefore returns how many entity_ops rows are older than before. It
// backs the dry-run path of cmd/prune and never deletes anything.
func (r *MaintenanceRepository) CountOpsBefore(ctx context.Context, before time.Time) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Raw(`SELECT COUNT(*) FROM entity_ops WHERE created_at < ?`, before).
		Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("count entity_ops before %s: %w", before.UTC().Format(time.RFC3339), err)
	}
	return count, nil
}

// PruneOps deletes entity_ops rows older than before in a single statement and
// returns the number of rows removed. A retry older than the cutoff loses
// idempotency protection and is applied again, which is why the cutoff must be
// far longer than any realistic client retry window.
func (r *MaintenanceRepository) PruneOps(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Exec(`DELETE FROM entity_ops WHERE created_at < ?`, before)
	if result.Error != nil {
		return 0, fmt.Errorf("prune entity_ops before %s: %w", before.UTC().Format(time.RFC3339), result.Error)
	}
	return result.RowsAffected, nil
}
