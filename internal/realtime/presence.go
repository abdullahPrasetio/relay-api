package realtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Presence is one member's last-known activity in a workspace. EntityID is the
// node the member is looking at, when the client reported one.
type Presence struct {
	WorkspaceID string    `json:"workspaceId"`
	UserID      uuid.UUID `json:"userId"`
	EntityID    *string   `json:"entityId,omitempty"`
	LastSeen    time.Time `json:"lastSeen"`
}

// PresenceStore persists and reads workspace presence.
type PresenceStore interface {
	// UpsertPresence records that user touched the workspace at p.LastSeen.
	UpsertPresence(ctx context.Context, p Presence) error
	// PresenceSince returns members whose LastSeen is at or after since,
	// newest first.
	PresenceSince(ctx context.Context, workspaceID string, since time.Time) ([]Presence, error)
}

type postgresPresenceStore struct {
	db *sql.DB
}

// NewPresenceStore returns the PostgreSQL-backed presence store.
func NewPresenceStore(db *sql.DB) PresenceStore {
	return &postgresPresenceStore{db: db}
}

func (s *postgresPresenceStore) UpsertPresence(ctx context.Context, p Presence) error {
	workspaceID := strings.TrimSpace(p.WorkspaceID)
	if workspaceID == "" {
		return ErrEmptyWorkspaceID
	}
	if p.UserID == uuid.Nil {
		return errors.New("realtime: presence user id is required")
	}
	if p.LastSeen.IsZero() {
		p.LastSeen = time.Now()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO presence (workspace_id, user_id, entity_id, last_seen)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (workspace_id, user_id) DO UPDATE
		SET entity_id = EXCLUDED.entity_id,
		    last_seen = EXCLUDED.last_seen`,
		workspaceID, p.UserID, p.EntityID, p.LastSeen.UTC(),
	)
	if err != nil {
		return fmt.Errorf("realtime: upsert presence for workspace %q: %w", workspaceID, err)
	}
	return nil
}

func (s *postgresPresenceStore) PresenceSince(ctx context.Context, workspaceID string, since time.Time) ([]Presence, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrEmptyWorkspaceID
	}
	if ctx == nil {
		ctx = context.Background()
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT workspace_id, user_id, entity_id, last_seen
		FROM presence
		WHERE workspace_id = $1 AND last_seen >= $2
		ORDER BY last_seen DESC
		LIMIT 200`,
		workspaceID, since.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("realtime: list presence for workspace %q: %w", workspaceID, err)
	}
	defer rows.Close() //nolint:errcheck

	out := make([]Presence, 0, 8)
	for rows.Next() {
		var row Presence
		var entityID sql.NullString
		if err := rows.Scan(&row.WorkspaceID, &row.UserID, &entityID, &row.LastSeen); err != nil {
			return nil, fmt.Errorf("realtime: scan presence: %w", err)
		}
		if entityID.Valid {
			id := entityID.String
			row.EntityID = &id
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("realtime: read presence: %w", err)
	}
	return out, nil
}
