//go:build integration

package usecase

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/migrations"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// These tests need a dedicated PostgreSQL database:
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/relay_auth?sslmode=disable' \
//	  go test -tags=integration ./internal/usecase/...
func integrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	dropAll := func() {
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
	dropAll()
	t.Cleanup(dropAll)

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	require.NoError(t, err)
	require.NoError(t, m.Up())
	t.Cleanup(func() { _, _ = m.Close() })

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return gdb
}

func TestSessionRepositoryIntegration(t *testing.T) {
	gdb := integrationDB(t)
	ctx := context.Background()

	users := dbrepo.NewUserRepository(gdb)
	sessions := dbrepo.NewSessionRepository(gdb)

	user := &entity.User{Name: "Repo", Email: "repo@example.com", Password: "hash"}
	require.NoError(t, users.Create(ctx, user))

	now := time.Now().UTC().Truncate(time.Microsecond)
	session := &entity.Session{
		UserID:      user.ID,
		DeviceLabel: "device-a",
		RefreshHash: "hash-1",
		ExpiresAt:   now.Add(30 * 24 * time.Hour),
		CreatedAt:   now,
		LastUsedAt:  &now,
	}
	require.NoError(t, sessions.Create(ctx, session))
	require.NotEqual(t, [16]byte{}, [16]byte(session.ID))

	found, err := sessions.FindByRefreshHash(ctx, "hash-1")
	require.NoError(t, err)
	assert.Equal(t, session.ID, found.ID)
	assert.Equal(t, user.ID, found.UserID)

	_, err = sessions.FindByRefreshHash(ctx, "missing")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Compare-and-swap rotation.
	prevExp := now.Add(120 * time.Second)
	usedAt := now.Add(time.Second)
	replacement := []byte{1, 2, 3, 4}
	ok, err := sessions.Rotate(ctx, session.ID, "hash-1", "hash-2", replacement, prevExp, now.Add(720*time.Hour), usedAt, "device-b")
	require.NoError(t, err)
	require.True(t, ok)

	current, err := sessions.FindByRefreshHash(ctx, "hash-2")
	require.NoError(t, err)
	assert.Equal(t, "device-b", current.DeviceLabel)
	assert.NotNil(t, current.PrevHash)
	assert.Equal(t, "hash-1", *current.PrevHash)
	require.NotNil(t, current.PrevExpiresAt)
	assert.True(t, current.PrevExpiresAt.Equal(prevExp))
	assert.Equal(t, replacement, current.ReplacementCT)
	require.NotNil(t, current.LastUsedAt)
	assert.True(t, current.LastUsedAt.Equal(usedAt))

	// The stale hash no longer matches, so a racing rotation loses cleanly.
	ok, err = sessions.Rotate(ctx, session.ID, "hash-1", "hash-3", nil, prevExp, now, usedAt, "")
	require.NoError(t, err)
	assert.False(t, ok)

	byPrev, err := sessions.FindByPrevHash(ctx, "hash-1")
	require.NoError(t, err)
	assert.Equal(t, session.ID, byPrev.ID)

	require.NoError(t, sessions.Touch(ctx, session.ID, now.Add(2*time.Second)))
	touched, err := sessions.FindByID(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, touched.LastUsedAt)

	list, err := sessions.ListByUser(ctx, user.ID, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Expired sessions are excluded from the active device list.
	list, err = sessions.ListByUser(ctx, user.ID, now.Add(31*24*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, list)

	require.NoError(t, sessions.RevokeByID(ctx, session.ID))
	revoked, err := sessions.FindByID(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)

	list, err = sessions.ListByUser(ctx, user.ID, now.Add(4*time.Second))
	require.NoError(t, err)
	assert.Empty(t, list)

	// Revoke every session for the user.
	second := &entity.Session{
		UserID: user.ID, RefreshHash: "hash-9", ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
	}
	require.NoError(t, sessions.Create(ctx, second))
	require.NoError(t, sessions.RevokeByUser(ctx, user.ID))
	list, err = sessions.ListByUser(ctx, user.ID, now.Add(5*time.Second))
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestAuthFlowIntegration(t *testing.T) {
	gdb := integrationDB(t)
	ctx := context.Background()

	users := dbrepo.NewUserRepository(gdb)
	sessions := dbrepo.NewSessionRepository(gdb)
	userUC := NewUserUseCase(users)

	jwtCfg := &auth.Config{
		Secret:   strings.Repeat("i", 32),
		Issuer:   "relay-test",
		Audience: "relay-api",
		Expiry:   15 * time.Minute,
	}
	authUC := NewAuthUseCase(users, userUC, sessions, jwtCfg, AuthOptions{
		AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour, GracePeriod: 120 * time.Second,
	}).(*authUseCase)
	clock := &testClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	authUC.now = clock.Now

	// register
	user, err := authUC.Register(ctx, &RegisterRequest{
		Name: "Integration", Email: "integration@example.com", Password: "password123",
	})
	require.NoError(t, err)
	require.NotEqual(t, [16]byte{}, [16]byte(user.ID))

	// login
	login, err := authUC.Login(ctx, &LoginRequest{
		Email: "integration@example.com", Password: "password123", DeviceLabel: "MacBook",
	})
	require.NoError(t, err)
	claims, err := auth.Verify(login.AccessToken, jwtCfg)
	require.NoError(t, err)
	assert.Equal(t, user.ID.String(), claims.Subject)

	// refresh: rotates the opaque token
	clock.Advance(time.Second)
	rotated, err := authUC.Refresh(ctx, &RefreshRequest{RefreshToken: login.RefreshToken})
	require.NoError(t, err)
	require.NotEqual(t, login.RefreshToken, rotated.RefreshToken)

	var stored entity.Session
	require.NoError(t, gdb.First(&stored).Error)
	assert.NotEqual(t, rotated.RefreshToken, stored.RefreshHash)
	assert.NotEqual(t, login.RefreshToken, stored.RefreshHash)
	require.NotEmpty(t, stored.ReplacementCT)
	assert.NotContains(t, string(stored.ReplacementCT), rotated.RefreshToken,
		"replacement pair must be encrypted at rest")

	// old token inside grace returns the exact same pair
	clock.Advance(30 * time.Second)
	replayed, err := authUC.Refresh(ctx, &RefreshRequest{RefreshToken: login.RefreshToken})
	require.NoError(t, err)
	assert.Equal(t, rotated.AccessToken, replayed.AccessToken)
	assert.Equal(t, rotated.RefreshToken, replayed.RefreshToken)

	// reuse after grace: 401 + whole session revoked
	clock.Advance(121 * time.Second)
	_, err = authUC.Refresh(ctx, &RefreshRequest{RefreshToken: login.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)

	var revoked entity.Session
	require.NoError(t, gdb.First(&revoked).Error)
	require.NotNil(t, revoked.RevokedAt)
	_, err = authUC.Refresh(ctx, &RefreshRequest{RefreshToken: rotated.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)

	devices, err := authUC.ListDevices(ctx, user.ID.String())
	require.NoError(t, err)
	assert.Empty(t, devices)

	// fresh login -> logout -> refresh is rejected
	fresh, err := authUC.Login(ctx, &LoginRequest{
		Email: "integration@example.com", Password: "password123", DeviceLabel: "phone",
	})
	require.NoError(t, err)
	require.NoError(t, authUC.Logout(ctx, fresh.RefreshToken))
	_, err = authUC.Refresh(ctx, &RefreshRequest{RefreshToken: fresh.RefreshToken})
	assert.ErrorIs(t, err, ErrInvalidRefresh)

	// /me still resolves the user from the access subject.
	me, err := authUC.Me(ctx, claims.Subject)
	require.NoError(t, err)
	assert.Equal(t, "integration@example.com", me.Email)
}
