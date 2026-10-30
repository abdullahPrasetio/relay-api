package entity

import (
	"time"

	"github.com/google/uuid"
)

// Comment is a note attached to one synced entity (usually a request).
//
// ID is a server-generated UUID; EntityID is the opaque workspace-local id
// from the entities table. ResolvedAt is nil while the thread is open.
// Mentions and AuthorName are hydrated by the repository on read.
type Comment struct {
	ID          uuid.UUID        `json:"id"`
	WorkspaceID string           `json:"workspaceId"`
	EntityID    string           `json:"entityId"`
	UserID      uuid.UUID        `json:"userId"`
	AuthorName  string           `json:"authorName"`
	Body        string           `json:"body"`
	ResolvedAt  *time.Time       `json:"resolvedAt,omitempty"`
	Mentions    []CommentMention `json:"mentions"`
	CreatedAt   time.Time        `json:"createdAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
}

// CommentMention is one user mentioned by a comment. Name is the user's
// display name at read time, so clients never need a second lookup.
type CommentMention struct {
	UserID uuid.UUID `json:"userId"`
	Name   string    `json:"name"`
}
