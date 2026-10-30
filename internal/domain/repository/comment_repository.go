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

// CommentRepository persists per-entity comment threads and their mention
// sets. Mutations also append an audit event in the same transaction, and
// reads hydrate the author name and mentioned users.
type CommentRepository interface {
	// CreateComment inserts the comment and the given mention set atomically.
	// The comment is hydrated (authorName, mentions) before returning.
	CreateComment(ctx context.Context, comment *entity.Comment, mentionIDs []uuid.UUID) error
	GetComment(ctx context.Context, id uuid.UUID) (*entity.Comment, error)

	// ListComments returns at most limit comments newest-first, optionally
	// after the comment identified by before (exclusive keyset cursor).
	ListComments(ctx context.Context, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, error)

	// ListCommentsMentioning returns at most limit comments of the workspace
	// that mention userID, newest-first, after the given cursor.
	ListCommentsMentioning(ctx context.Context, workspaceID string, userID uuid.UUID, before *uuid.UUID, limit int) ([]entity.Comment, error)

	// UpdateComment persists the mutable fields and records the given audit
	// action (comment.update / comment.resolve / comment.unresolve). When
	// mentionIDs is non-nil the mention set is replaced with it atomically;
	// nil leaves the set untouched. The comment is hydrated before returning.
	UpdateComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID, action string, mentionIDs *[]uuid.UUID) error

	DeleteComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID) error

	// EntityExists reports whether entityID is a live entity in the workspace.
	EntityExists(ctx context.Context, workspaceID, entityID string) (bool, error)

	// WorkspaceMemberNames returns the display names of the given users that
	// are current members of workspaceID, keyed by user id. Non-members are
	// simply absent from the map.
	WorkspaceMemberNames(ctx context.Context, workspaceID string, userIDs []uuid.UUID) (map[uuid.UUID]string, error)
}
