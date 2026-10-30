//go:build integration

package main

import (
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/migrations"
)

// Requires a reachable PostgreSQL database, e.g.:
//
//	MIGRATE_TEST_DSN='postgres://postgres@127.0.0.1:55432/relay_auth?sslmode=disable' \
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
		_, err := db.Exec(`
DO $$
DECLARE r record;
BEGIN
  FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP
    EXECUTE 'DROP TABLE IF EXISTS public.' || quote_ident(r.tablename) || ' CASCADE';
  END LOOP;
END $$`)
		require.NoError(t, err)
	}
	clean()
	t.Cleanup(clean)

	m, mdb, err := newMigrator("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mdb.Close() })

	// Every embedded migration applies cleanly.
	latest := latestVersion(t)
	require.NoError(t, m.Up())
	v, dirty, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, latest, v)
	require.False(t, dirty)
	require.True(t, tableExists(t, db, "users"))
	require.True(t, tableExists(t, db, "sessions"))

	// 000002 aligns users.id with the plan's UUID schema so later migrations
	// can reference users(id) with uuid foreign keys.
	var idType string
	require.NoError(t, db.QueryRow(
		`SELECT data_type FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'id'`,
	).Scan(&idType))
	require.Equal(t, "uuid", idType)

	// The schema is usable: a user and a session insert succeed.
	userID := "3f0a2e64-9c1b-4a8f-8c2e-8b1f4e6d9a01"
	_, err = db.Exec(
		`INSERT INTO users (id, name, email, password) VALUES ($1, $2, $3, $4)`,
		userID, "Alice", "alice@example.com", "hash",
	)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO sessions (id, user_id, device_label, refresh_hash, expires_at)
		 VALUES ($1, $2, $3, $4, now() + interval '30 days')`,
		"8b7c6d5e-4f3a-2b1c-9d8e-7f6a5b4c3d21", userID, "test", "abc",
	)
	require.NoError(t, err)

	// Running again without changes is a no-op, not an error.
	require.ErrorIs(t, m.Up(), migrate.ErrNoChange)

	// Down reverts everything, and up re-applies it.
	require.NoError(t, m.Down())
	require.False(t, tableExists(t, db, "users"))
	require.False(t, tableExists(t, db, "sessions"))
	require.NoError(t, m.Up())
	require.True(t, tableExists(t, db, "users"))
	require.True(t, tableExists(t, db, "sessions"))
}

func latestVersion(t *testing.T) uint {
	t.Helper()
	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	v, err := src.First()
	require.NoError(t, err)
	for {
		next, err := src.Next(v)
		if errors.Is(err, io.EOF) || errors.Is(err, fs.ErrNotExist) {
			return v
		}
		require.NoError(t, err)
		v = next
	}
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
