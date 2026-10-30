//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/migrations"
)

// Requires a reachable PostgreSQL database, e.g.:
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/hardening?sslmode=disable' \
//	  go test -tags=integration ./internal/integration/...
//
// This test lives in the same package as TestWorkspaceDeltaSync, so Go runs
// the two sequentially and each can reset the schema safely.
func TestPruneOpsRetention(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	// Fresh schema so migration 000004 (the created_at index) is applied.
	_, err = sqlDB.Exec(`DROP SCHEMA IF EXISTS public CASCADE`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE SCHEMA public`)
	require.NoError(t, err)

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	driver, err := migratepostgres.WithInstance(sqlDB, &migratepostgres.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	require.NoError(t, err)
	require.NoError(t, m.Up())

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if gormSQL, err := gdb.DB(); err == nil {
			_ = gormSQL.Close()
		}
	})

	ctx := context.Background()
	repo := db.NewMaintenanceRepository(gdb)

	var indexName string
	require.NoError(t, gdb.Raw(
		`SELECT indexname FROM pg_indexes WHERE tablename = 'entity_ops' AND indexname = 'idx_entity_ops_created_at'`,
	).Scan(&indexName).Error)
	require.Equal(t, "idx_entity_ops_created_at", indexName)

	ownerID := uuid.New()
	workspaceID := "ws_prune_" + ownerID.String()[:8]
	require.NoError(t, gdb.Exec(
		`INSERT INTO users (id, name, email, password) VALUES (?, ?, ?, ?)`,
		ownerID, "Prune", "prune-"+ownerID.String()+"@example.test", "hash",
	).Error)
	require.NoError(t, gdb.Exec(
		`INSERT INTO workspaces (id, name, owner_id) VALUES (?, ?, ?)`,
		workspaceID, "Prune Workspace", ownerID,
	).Error)

	// Millisecond precision: Postgres timestamptz is microsecond-precise, so
	// the exactly-at-cutoff row compares equal and must be retained by the
	// strict "created_at < cutoff" predicate.
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour).Truncate(time.Millisecond)

	insertOp := func(opID string, createdAt time.Time) {
		t.Helper()
		require.NoError(t, gdb.Exec(
			`INSERT INTO entity_ops (workspace_id, op_id, entity_id, rev, result, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			workspaceID, opID, "req_1", 1, `{"applied":true}`, createdAt,
		).Error)
	}

	// All three are strictly older than the cutoff.
	insertOp("op_old_2h", cutoff.Add(-2*time.Hour))
	insertOp("op_old_1h", cutoff.Add(-time.Hour))
	insertOp("op_old_1s", cutoff.Add(-time.Second))
	// Kept: exactly at the cutoff and newer.
	insertOp("op_boundary", cutoff)
	insertOp("op_recent_1s", cutoff.Add(time.Second))
	insertOp("op_recent_1h", cutoff.Add(time.Hour))

	// An entity_changes row exists alongside; pruning entity_ops must not
	// touch the pull log.
	require.NoError(t, gdb.Exec(
		`INSERT INTO entity_changes (workspace_id, entity_id, rev, deleted) VALUES (?, ?, ?, ?)`,
		workspaceID, "req_1", 1, false,
	).Error)

	// Dry-run path: count only, nothing deleted.
	candidates, err := repo.CountOpsBefore(ctx, cutoff)
	require.NoError(t, err)
	require.EqualValues(t, 3, candidates, "candidate rows strictly older than the cutoff")

	var total int64
	require.NoError(t, gdb.Raw(`SELECT COUNT(*) FROM entity_ops WHERE workspace_id = ?`, workspaceID).Scan(&total).Error)
	require.EqualValues(t, 6, total, "dry-run must not delete rows")

	// Real prune removes exactly the rows before the cutoff.
	removed, err := repo.PruneOps(ctx, cutoff)
	require.NoError(t, err)
	require.EqualValues(t, 3, removed)

	var remaining []string
	require.NoError(t, gdb.Raw(
		`SELECT op_id FROM entity_ops WHERE workspace_id = ? ORDER BY op_id`, workspaceID,
	).Scan(&remaining).Error)
	require.Equal(t, []string{"op_boundary", "op_recent_1h", "op_recent_1s"}, remaining)

	var changes int64
	require.NoError(t, gdb.Raw(`SELECT COUNT(*) FROM entity_changes WHERE workspace_id = ?`, workspaceID).Scan(&changes).Error)
	require.EqualValues(t, 1, changes, "entity_changes must never be pruned")

	// Pruning again is a no-op.
	removed, err = repo.PruneOps(ctx, cutoff)
	require.NoError(t, err)
	require.Zero(t, removed)
}
