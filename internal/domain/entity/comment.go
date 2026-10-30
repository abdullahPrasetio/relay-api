package entity

import (
	"time"

	"github.com/google/uuid"
)

// Comment is a note attached to one synced entity (usually a request).
//
// ID is a server-generated UUID; EntityID is the opaque workspace-local id
// from the entities table. ResolvedAt is nil while the thread is open.
type Comment struct {
	ID          uuid.UUID  `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	EntityID    string     `json:"entityId"`
	UserID      uuid.UUID  `json:"userId"`
	Body        string     `json:"body"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}
