package entity

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Invite is a single-use workspace invitation. Only the SHA-256 hash of the
// token is persisted; the raw token is returned to the creator exactly once.
type Invite struct {
	ID          uuid.UUID
	WorkspaceID string
	Role        Role
	TokenHash   string
	CreatedBy   uuid.UUID
	ExpiresAt   time.Time
	AcceptedAt  *time.Time
	AcceptedBy  *uuid.UUID
	CreatedAt   time.Time
}

// InviteInfo is the unauthenticated metadata view of an invite. It never
// contains the token hash or the raw token.
type InviteInfo struct {
	WorkspaceID   string    `json:"workspaceId"`
	WorkspaceName string    `json:"workspaceName"`
	Role          Role      `json:"role"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// AuditEvent is one entry of the append-only workspace audit log.
type AuditEvent struct {
	ID          int64           `json:"id"`
	WorkspaceID string          `json:"workspaceId"`
	ActorID     uuid.UUID       `json:"actorId"`
	Action      string          `json:"action"`
	Target      string          `json:"target"`
	Meta        json.RawMessage `json:"meta"`
	CreatedAt   time.Time       `json:"createdAt"`
}
