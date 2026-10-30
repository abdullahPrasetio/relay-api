// Package realtime carries append-log change signals between API instances and
// serves live workspace presence.
//
// The change signal is deliberately small: PostgreSQL NOTIFY/LISTEN carries
// only {workspaceId, seq, at}. Clients treat the sequence as a hint and pull
// the actual delta page from the existing /changes endpoint with their own
// cursor, so a dropped notification can never lose data.
package realtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" database/sql driver

	"github.com/abdullahPrasetio/relay-api/config"
)

// ChannelWorkspaceChanges is the PostgreSQL notification channel used for
// workspace change signals.
const ChannelWorkspaceChanges = "workspace_changes"

// ErrEmptyWorkspaceID is returned when an operation needs a workspace id and
// got an empty string.
var ErrEmptyWorkspaceID = errors.New("realtime: workspace id is required")

// ErrNotifierClosed is returned by Notify after Close on an owned connection.
var ErrNotifierClosed = errors.New("realtime: notifier is closed")

// ChangeEvent is the JSON payload published on ChannelWorkspaceChanges.
type ChangeEvent struct {
	WorkspaceID string    `json:"workspaceId"`
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
}

// Notifier publishes workspace change signals with pg_notify.
//
// It is safe for concurrent use: PostgreSQL connections are multiplexed by
// database/sql and pg_notify is a plain function call.
type Notifier struct {
	db     *sql.DB
	ownsDB bool
	closed atomic.Bool
	now    func() time.Time
}

// NewNotifier wraps an existing *sql.DB. The caller keeps ownership of the
// pool, so Close is a no-op. Prefer this in the API binary, which already holds
// the shared database/sql pool.
func NewNotifier(db *sql.DB) *Notifier {
	return &Notifier{db: db, now: time.Now}
}

// NewNotifierFromDSN opens and verifies a dedicated PostgreSQL pool for
// notifications. Close releases it.
func NewNotifierFromDSN(dsn string) (*Notifier, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%w: empty DSN", ErrInvalidDSN)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("realtime: open notifier connection: %w", err)
	}
	// Notifications are cheap, single-statement calls: a tiny pool is enough
	// and keeps a shared API instance from starving the request pool.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("realtime: ping notifier connection: %w", err)
	}
	return &Notifier{db: db, ownsDB: true, now: time.Now}, nil
}

// NewNotifierFromConfig opens a dedicated PostgreSQL pool from the API DB
// configuration. Use NewNotifier when a pool already exists.
func NewNotifierFromConfig(cfg *config.DBConfig) (*Notifier, error) {
	dsn, err := DSNFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return NewNotifierFromDSN(dsn)
}

// Notify publishes {workspaceId, seq, at} on the workspace_changes channel.
//
// Call it after the push transaction has committed: PostgreSQL only delivers a
// pg_notify once its transaction commits, and a rejected push must not wake
// clients. A failed notification is a hint loss, never a data loss.
func (n *Notifier) Notify(ctx context.Context, workspaceID string, seq int64) error {
	if n == nil || n.closed.Load() {
		return ErrNotifierClosed
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return ErrEmptyWorkspaceID
	}
	if seq < 0 {
		return fmt.Errorf("realtime: sequence must be non-negative, got %d", seq)
	}
	if n.db == nil {
		return ErrNotifierClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	payload, err := json.Marshal(ChangeEvent{
		WorkspaceID: workspaceID,
		Seq:         seq,
		At:          n.now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("realtime: marshal change event: %w", err)
	}

	if _, err := n.db.ExecContext(ctx, `SELECT pg_notify($1, $2)`, ChannelWorkspaceChanges, string(payload)); err != nil {
		return fmt.Errorf("realtime: notify workspace %q seq %d: %w", workspaceID, seq, err)
	}
	return nil
}

// Close releases the pool when the notifier owns it.
func (n *Notifier) Close() error {
	if n == nil {
		return nil
	}
	n.closed.Store(true)
	if !n.ownsDB || n.db == nil {
		return nil
	}
	return n.db.Close()
}
