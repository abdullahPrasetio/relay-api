package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

// ErrCommentCursorNotFound means the `before` cursor does not identify a
// comment in the requested workspace/entity feed. Callers SHOULD map it to a
// validation error rather than leaking which ids exist elsewhere.
var ErrCommentCursorNotFound = errors.New("comment cursor not found")

// Comment audit actions. They are stable API values, shared by the usecase
// (which picks resolve/unresolve) and the repository (which writes the row).
const (
	CommentActionCreate    = "comment.create"
	CommentActionUpdate    = "comment.update"
	CommentActionResolve   = "comment.resolve"
	CommentActionUnresolve = "comment.unresolve"
	CommentActionDelete    = "comment.delete"
)

// CommentRepository persists per-entity comment threads. Mutations also append
// an audit event in the same transaction.
type CommentRepository interface {
	CreateComment(ctx context.Context, comment *entity.Comment) error
	GetComment(ctx context.Context, id uuid.UUID) (*entity.Comment, error)

	// ListComments returns at most limit comments newest-first, optionally
	// after the comment identified by before (exclusive keyset cursor).
	ListComments(ctx context.Context, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, error)

	// UpdateComment persists the mutable fields and records the given audit
	// action (comment.update / comment.resolve / comment.unresolve).
	UpdateComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID, action string) error

	DeleteComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID) error

	// EntityExists reports whether entityID is a live entity in the workspace.
	EntityExists(ctx context.Context, workspaceID, entityID string) (bool, error)
}
