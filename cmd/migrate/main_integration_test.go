//go:build integration

package main

import (
	"database/sql"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Requires a reachable PostgreSQL database, e.g.:
//
//	MIGRATE_TEST_DSN='postgres://postgres@127.0.0.1:55432/wapgo_test?sslmode=disable' \
//	  go test -tags=integration ./cmd/migrate/...
func TestMigrateUpDownPostgres(t *testing.T) {
	dsn := os.Getenv("MIGRATE_TEST_DSN")
	if dsn == "" {
		t.Skip("MIGRATE_TEST_DSN not set")
	}

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	clean := func() {
		_, err := db.Exec(`DROP TABLE IF EXISTS users, schema_migrations CASCADE`)
		require.NoError(t, err)
	}
	clean()
	t.Cleanup(clean)

	m, mdb, err := newMigrator("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mdb.Close() })

	// First run applies version 1 and creates the table.
	require.NoError(t, m.Up())
	v, dirty, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(1), v)
	require.False(t, dirty)
	require.True(t, tableExists(t, db, "users"))

	// The schema is usable: required columns exist and inserts succeed.
	_, err = db.Exec(
		`INSERT INTO users (id, name, email, password) VALUES ($1, $2, $3, $4)`,
		"u1", "Alice", "alice@example.com", "hash",
	)
	require.NoError(t, err)

	// Running again without changes is a no-op, not an error.
	require.ErrorIs(t, m.Up(), migrate.ErrNoChange)

	// Down reverts the table, and up re-applies it.
	require.NoError(t, m.Steps(-1))
	require.False(t, tableExists(t, db, "users"))
	require.NoError(t, m.Up())
	require.True(t, tableExists(t, db, "users"))
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`,
		name,
	).Scan(&n)
	require.NoError(t, err)
	return n == 1
}
