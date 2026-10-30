//go:build integration

package realtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/internal/realtime"
)

// These tests need a reachable PostgreSQL database with the phase-2 schema:
//
//	DB_DRIVER=postgres DB_HOST=127.0.0.1 DB_PORT=55432 DB_USER=postgres \
//	  DB_NAME=phase2_realtime DB_SSL_MODE=disable go run ./cmd/migrate -dir up
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/phase2_realtime?sslmode=disable' \
//	  go test -tags=integration ./internal/realtime/...
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return dsn
}

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := integrationDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	return db
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%s", prefix, uuid.NewString())
}

// receive takes exactly one sequence from ch or fails the test on timeout.
func receive(t *testing.T, ch <-chan int64, timeout time.Duration) int64 {
	t.Helper()
	select {
	case seq, ok := <-ch:
		require.True(t, ok, "subscription channel closed while waiting for a signal")
		return seq
	case <-time.After(timeout):
		t.Fatalf("no change signal within %s", timeout)
		return 0
	}
}

func requireClosed(t *testing.T, ch <-chan int64, timeout time.Duration) {
	t.Helper()
	select {
	case seq, ok := <-ch:
		require.False(t, ok, "expected the subscription channel to close, got seq %d", seq)
	case <-time.After(timeout):
		t.Fatalf("subscription channel was not closed within %s", timeout)
	}
}

func TestNotifierListenerRoundTrip(t *testing.T) {
	db := openIntegrationDB(t)
	dsn := integrationDSN(t)

	baseline := runtime.NumGoroutine()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	listener, err := realtime.NewListener(rootCtx, dsn,
		realtime.WithApplicationName(uniqueName("relay-rt-roundtrip")),
		realtime.WithBackoff(50*time.Millisecond, 500*time.Millisecond),
	)
	require.NoError(t, err)

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readyCancel()
	require.NoError(t, listener.WaitReady(readyCtx))

	wsA := uniqueName("ws_rt_a")
	wsB := uniqueName("ws_rt_b")

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	chA, err := listener.Listen(subCtx, wsA)
	require.NoError(t, err)
	chB, err := listener.Listen(subCtx, wsB)
	require.NoError(t, err)

	notifier := realtime.NewNotifier(db)
	ctx := context.Background()

	// A notification reaches only the matching workspace subscription.
	require.NoError(t, notifier.Notify(ctx, wsA, 42))
	require.EqualValues(t, 42, receive(t, chA, 2*time.Second))

	select {
	case seq := <-chB:
		t.Fatalf("workspace B received workspace A's sequence %d", seq)
	case <-time.After(150 * time.Millisecond):
	}

	require.NoError(t, notifier.Notify(ctx, wsB, 7))
	require.EqualValues(t, 7, receive(t, chB, 2*time.Second))

	// Cancelling a subscription closes its channel and releases the watcher.
	subCancel()
	requireClosed(t, chA, 2*time.Second)
	requireClosed(t, chB, 2*time.Second)

	require.NoError(t, listener.Close())

	// The listener goroutine and the dedicated pq connection must be gone.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	}, 5*time.Second, 50*time.Millisecond, "listener leaked goroutines after Close")
}

func TestListenerReconnectsAfterConnectionLoss(t *testing.T) {
	db := openIntegrationDB(t)
	dsn := integrationDSN(t)

	appName := uniqueName("relay-rt-reconnect")
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	listener, err := realtime.NewListener(rootCtx, dsn,
		realtime.WithApplicationName(appName),
		realtime.WithBackoff(50*time.Millisecond, 300*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readyCancel()
	require.NoError(t, listener.WaitReady(readyCtx))

	ws := uniqueName("ws_rt_reconnect")
	ch, err := listener.Listen(rootCtx, ws)
	require.NoError(t, err)

	notifier := realtime.NewNotifier(db)
	ctx := context.Background()

	// Sanity: the healthy connection delivers.
	require.NoError(t, notifier.Notify(ctx, ws, 1))
	require.EqualValues(t, 1, receive(t, ch, 2*time.Second))

	// Kill the listener's dedicated backend and let it reconnect.
	var terminated sql.NullBool
	err = db.QueryRowContext(ctx, `
		SELECT bool_or(pg_terminate_backend(pid))
		FROM pg_stat_activity
		WHERE application_name = $1 AND pid <> pg_backend_pid()`,
		appName,
	).Scan(&terminated)
	require.NoError(t, err)
	require.True(t, terminated.Valid && terminated.Bool, "listener backend was not found in pg_stat_activity")

	// The listener reconnects with backoff; poll with fresh sequences until one
	// arrives again (signals sent while disconnected are intentionally lost).
	deadline := time.Now().Add(15 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		seq := time.Now().UnixNano()
		require.NoError(t, notifier.Notify(ctx, ws, seq))
		select {
		case received, ok := <-ch:
			require.True(t, ok, "subscription channel closed during reconnect")
			got = received
		case <-time.After(100 * time.Millisecond):
			continue
		}
		break
	}
	require.Positive(t, got, "listener did not receive any signal after the connection was terminated")
}

func TestPresenceStoreUpsertAndTTL(t *testing.T) {
	db := openIntegrationDB(t)
	store := realtime.NewPresenceStore(db)
	ctx := context.Background()

	workspaceID := uniqueName("ws_rt_presence")
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM presence WHERE workspace_id = $1`, workspaceID)
	})

	userA := uuid.New()
	userB := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	entity := "req_1"
	require.NoError(t, store.UpsertPresence(ctx, realtime.Presence{
		WorkspaceID: workspaceID,
		UserID:      userA,
		EntityID:    &entity,
		LastSeen:    now.Add(-90 * time.Second),
	}))

	// A stale member is outside the 60s window.
	require.NoError(t, store.UpsertPresence(ctx, realtime.Presence{
		WorkspaceID: workspaceID,
		UserID:      userB,
		LastSeen:    now.Add(-2 * time.Minute),
	}))

	// Upsert the same (workspace, user): latest entity and timestamp win.
	moved := "env_2"
	require.NoError(t, store.UpsertPresence(ctx, realtime.Presence{
		WorkspaceID: workspaceID,
		UserID:      userA,
		EntityID:    &moved,
		LastSeen:    now,
	}))

	rows, err := store.PresenceSince(ctx, workspaceID, now.Add(-60*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, userA, rows[0].UserID)
	require.NotNil(t, rows[0].EntityID)
	require.Equal(t, "env_2", *rows[0].EntityID)
	require.WithinDuration(t, now, rows[0].LastSeen, time.Millisecond)

	// Clearing the entity keeps the row but drops the pointer.
	require.NoError(t, store.UpsertPresence(ctx, realtime.Presence{
		WorkspaceID: workspaceID,
		UserID:      userA,
		LastSeen:    now.Add(time.Second),
	}))
	rows, err = store.PresenceSince(ctx, workspaceID, now.Add(-60*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].EntityID)

	// A second workspace never leaks into this one's presence.
	otherRows, err := store.PresenceSince(ctx, uniqueName("ws_rt_other"), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Empty(t, otherRows)
}

func TestListenerContextCancelClosesSubscriptions(t *testing.T) {
	dsn := integrationDSN(t)

	baseline := runtime.NumGoroutine()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	listener, err := realtime.NewListener(rootCtx, dsn,
		realtime.WithApplicationName(uniqueName("relay-rt-cancel")),
		realtime.WithBackoff(50*time.Millisecond, 500*time.Millisecond),
	)
	require.NoError(t, err)

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readyCancel()
	require.NoError(t, listener.WaitReady(readyCtx))

	// The subscription uses its own context: cancelling the listener's root
	// context must still close it, without the caller calling Close.
	ch, err := listener.Listen(context.Background(), uniqueName("ws_rt_cancel"))
	require.NoError(t, err)

	rootCancel()
	requireClosed(t, ch, 5*time.Second)

	require.NoError(t, listener.Close())
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	}, 5*time.Second, 50*time.Millisecond, "cancelled listener leaked goroutines")
}

func TestListenerConcurrentSubscriptions(t *testing.T) {
	db := openIntegrationDB(t)
	dsn := integrationDSN(t)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	listener, err := realtime.NewListener(rootCtx, dsn,
		realtime.WithApplicationName(uniqueName("relay-rt-fanout")),
		realtime.WithBackoff(50*time.Millisecond, 500*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readyCancel()
	require.NoError(t, listener.WaitReady(readyCtx))

	ws := uniqueName("ws_rt_fanout")
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	const subscribers = 4
	channels := make([]<-chan int64, subscribers)
	for i := range subscribers {
		channels[i], err = listener.Listen(subCtx, ws)
		require.NoError(t, err)
	}

	notifier := realtime.NewNotifier(db)
	require.NoError(t, notifier.Notify(context.Background(), ws, 99))

	for _, ch := range channels {
		require.EqualValues(t, 99, receive(t, ch, 2*time.Second))
	}

	// Cancelling one subscriber leaves the others intact.
	oneCtx, oneCancel := context.WithCancel(context.Background())
	extra, err := listener.Listen(oneCtx, ws)
	require.NoError(t, err)
	oneCancel()
	requireClosed(t, extra, 2*time.Second)

	require.NoError(t, notifier.Notify(context.Background(), ws, 100))
	for _, ch := range channels {
		require.EqualValues(t, 100, receive(t, ch, 2*time.Second))
	}
}
