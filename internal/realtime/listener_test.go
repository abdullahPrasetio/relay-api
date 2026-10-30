package realtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/config"
)

func TestNextBackoffDoublesAndCaps(t *testing.T) {
	require.Equal(t, 200*time.Millisecond, nextBackoff(100*time.Millisecond, time.Second))
	require.Equal(t, time.Second, nextBackoff(800*time.Millisecond, time.Second))
	require.Equal(t, defaultMaxBackoff, nextBackoff(defaultMaxBackoff, 0))
}

func TestDecorateListenerDSNURL(t *testing.T) {
	dsn := decorateListenerDSN(
		"postgres://postgres@127.0.0.1:55432/relay?sslmode=disable",
		"relay-test-listener",
	)
	require.Contains(t, dsn, "application_name=relay-test-listener")
	require.Contains(t, dsn, "connect_timeout=5")
	require.Contains(t, dsn, "sslmode=disable")

	// Existing values win.
	explicit := decorateListenerDSN(
		"postgres://postgres@127.0.0.1:55432/relay?application_name=mine&connect_timeout=1",
		"relay-test-listener",
	)
	require.Contains(t, explicit, "application_name=mine")
	require.Contains(t, explicit, "connect_timeout=1")
}

func TestDecorateListenerDSNKeywordValue(t *testing.T) {
	dsn := decorateListenerDSN("user=postgres dbname=relay sslmode=disable", "relay-test-listener")
	require.True(t, strings.HasSuffix(dsn, " application_name=relay-test-listener connect_timeout=5"))
}

func TestSubscriptionCoalescesToNewest(t *testing.T) {
	sub := &subscription{ch: make(chan int64, 2)}
	sub.send(1)
	sub.send(2)
	sub.send(3)

	// Capacity is two, so the oldest hint was dropped and the newest is kept.
	require.EqualValues(t, 2, <-sub.ch)
	require.EqualValues(t, 3, <-sub.ch)

	sub.close()
	require.NotPanics(t, func() { sub.send(4) })
	_, ok := <-sub.ch
	require.False(t, ok, "closed subscription must not accept signals")
}

func TestNotifierRejectsInvalidInput(t *testing.T) {
	var nilNotifier *Notifier
	require.ErrorIs(t, nilNotifier.Notify(context.Background(), "ws_1", 1), ErrNotifierClosed)

	notifier := NewNotifier(nil)
	require.ErrorIs(t, notifier.Notify(context.Background(), "ws_1", 1), ErrNotifierClosed)
	require.ErrorIs(t, notifier.Notify(context.Background(), "  ", 1), ErrEmptyWorkspaceID)
	require.Error(t, notifier.Notify(context.Background(), "ws_1", -1))
}

func TestDSNFromConfigBuildsPostgresURL(t *testing.T) {
	dsn, err := DSNFromConfig(&config.DBConfig{
		Driver:  "postgres",
		Host:    "127.0.0.1",
		Port:    "55432",
		User:    "postgres",
		Name:    "relay",
		SSLMode: "disable",
	})
	require.NoError(t, err)
	require.Equal(t, "postgres://postgres@127.0.0.1:55432/relay?sslmode=disable", dsn)
}

func TestDSNFromConfigRejectsNonPostgres(t *testing.T) {
	_, err := DSNFromConfig(nil)
	require.ErrorIs(t, err, ErrInvalidDSN)

	_, err = DSNFromConfig(&config.DBConfig{Driver: "mysql", Host: "db", User: "u", Name: "n"})
	require.ErrorIs(t, err, ErrInvalidDSN)
}
