package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

const (
	commentBodyMaxRunes   = 4000
	defaultCommentLimit   = 50
	maxCommentLimit       = 100
	commentEntityRequired = "entityId is required"
)

// ErrCommentEntityNotFound means the target entity does not exist (or is
// soft-deleted) in the workspace. Handlers map it to 404.
var ErrCommentEntityNotFound = errors.New("comment entity not found")

// CreateCommentInput is the validated create payload.
type CreateCommentInput struct {
	EntityID string
	Body     string
}

// UpdateCommentInput uses pointers to distinguish "absent" from "clear".
type UpdateCommentInput struct {
	Body     *string
	Resolved *bool
}

// CommentUseCase is the per-entity comment service.
//
// Reading requires pull (viewer+). Posting only requires membership: every
// role, including viewer, may comment. Editing/removing is limited to the
// author or an admin+ (owner/admin), which is checked after membership so a
// non-member always sees 404.
type CommentUseCase interface {
	// List returns one page newest-first plus the `before` cursor for the next
	// page (empty when the page is the last one).
	List(ctx context.Context, userID uuid.UUID, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, string, error)
	Create(ctx context.Context, userID uuid.UUID, workspaceID string, input CreateCommentInput) (*entity.Comment, error)
	Update(ctx context.Context, userID uuid.UUID, commentID uuid.UUID, input UpdateCommentInput) (*entity.Comment, error)
	Delete(ctx context.Context, userID uuid.UUID, commentID uuid.UUID) error
}

type commentUseCase struct {
	workspaces WorkspaceAuthorizer
	comments   CommentStore
}

// CommentStore is the persistence surface used by the comment usecase.
type CommentStore interface {
	CreateComment(ctx context.Context, comment *entity.Comment) error
	GetComment(ctx context.Context, id uuid.UUID) (*entity.Comment, error)
	ListComments(ctx context.Context, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, error)
	UpdateComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID, action string) error
	DeleteComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID) error
	EntityExists(ctx context.Context, workspaceID, entityID string) (bool, error)
}

// NewCommentUseCase wires comment storage and workspace authorization.
func NewCommentUseCase(workspaces WorkspaceAuthorizer, comments CommentStore) CommentUseCase {
	return &commentUseCase{workspaces: workspaces, comments: comments}
}

func (u *commentUseCase) List(ctx context.Context, userID uuid.UUID, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, string, error) {
	member, err := u.member(ctx, userID, workspaceID)
	if err != nil {
		return nil, "", err
	}
	if !member.Role.Allows(entity.PermissionPull) {
		return nil, "", fmt.Errorf("%w: role %q cannot pull", ErrForbidden, member.Role)
	}

	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, "", fmt.Errorf("%w: %s", ErrValidation, commentEntityRequired)
	}
	limit = normalizeCommentLimit(limit)

	// Fetch one extra row to learn whether another page exists without
	// counting the whole thread.
	rows, err := u.comments.ListComments(ctx, workspaceID, entityID, before, limit+1)
	if err != nil {
		if errors.Is(err, domainrepo.ErrCommentCursorNotFound) {
			return nil, "", fmt.Errorf("%w: before cursor is not part of this entity feed", ErrValidation)
		}
		return nil, "", err
	}

	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID.String()
	}
	return rows, next, nil
}

func (u *commentUseCase) Create(ctx context.Context, userID uuid.UUID, workspaceID string, input CreateCommentInput) (*entity.Comment, error) {
	if _, err := u.member(ctx, userID, workspaceID); err != nil {
		return nil, err
	}

	entityID := strings.TrimSpace(input.EntityID)
	if entityID == "" {
		return nil, fmt.Errorf("%w: %s", ErrValidation, commentEntityRequired)
	}
	body, err := validCommentBody(input.Body)
	if err != nil {
		return nil, err
	}

	exists, err := u.comments.EntityExists(ctx, workspaceID, entityID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w: entity %q does not exist in this workspace", ErrCommentEntityNotFound, entityID)
	}

	comment := &entity.Comment{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		EntityID:    entityID,
		UserID:      userID,
		Body:        body,
	}
	if err := u.comments.CreateComment(ctx, comment); err != nil {
		return nil, mapCommentError(err)
	}
	return comment, nil
}

func (u *commentUseCase) Update(ctx context.Context, userID uuid.UUID, commentID uuid.UUID, input UpdateCommentInput) (*entity.Comment, error) {
	if input.Body == nil && input.Resolved == nil {
		return nil, fmt.Errorf("%w: body or resolved is required", ErrValidation)
	}

	comment, err := u.loadModeratable(ctx, userID, commentID)
	if err != nil {
		return nil, err
	}

	action := domainrepo.CommentActionUpdate
	if input.Body != nil {
		body, err := validCommentBody(*input.Body)
		if err != nil {
			return nil, err
		}
		comment.Body = body
	}
	if input.Resolved != nil {
		switch {
		case *input.Resolved && comment.ResolvedAt == nil:
			now := time.Now().UTC()
			comment.ResolvedAt = &now
			action = domainrepo.CommentActionResolve
		case !*input.Resolved && comment.ResolvedAt != nil:
			comment.ResolvedAt = nil
			action = domainrepo.CommentActionUnresolve
		}
	}

	if err := u.comments.UpdateComment(ctx, comment, userID, action); err != nil {
		return nil, mapCommentError(err)
	}
	return comment, nil
}

func (u *commentUseCase) Delete(ctx context.Context, userID uuid.UUID, commentID uuid.UUID) error {
	comment, err := u.loadModeratable(ctx, userID, commentID)
	if err != nil {
		return err
	}
	if err := u.comments.DeleteComment(ctx, comment, userID); err != nil {
		return mapCommentError(err)
	}
	return nil
}

// loadModeratable loads the comment and enforces "author or admin+". A
// non-member yields ErrNotFound (never revealing comment existence).
func (u *commentUseCase) loadModeratable(ctx context.Context, userID uuid.UUID, commentID uuid.UUID) (*entity.Comment, error) {
	comment, err := u.comments.GetComment(ctx, commentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	member, err := u.member(ctx, userID, comment.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if comment.UserID != userID && !canModerateComments(member.Role) {
		return nil, fmt.Errorf("%w: only the author or an admin may change this comment", ErrForbidden)
	}
	return comment, nil
}

func (u *commentUseCase) member(ctx context.Context, userID uuid.UUID, workspaceID string) (*entity.WorkspaceMember, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("%w: workspace id is required", ErrValidation)
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: user id is required", ErrValidation)
	}
	member, err := u.workspaces.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return member, nil
}

// canModerateComments reports whether role is admin or owner.
func canModerateComments(role entity.Role) bool {
	return role == entity.RoleAdmin || role == entity.RoleOwner
}

func normalizeCommentLimit(limit int) int {
	if limit <= 0 {
		return defaultCommentLimit
	}
	if limit > maxCommentLimit {
		return maxCommentLimit
	}
	return limit
}

// validCommentBody trims surrounding whitespace and enforces 1..4000 runes.
func validCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("%w: body is required", ErrValidation)
	}
	if len([]rune(body)) > commentBodyMaxRunes {
		return "", fmt.Errorf("%w: body must be at most %d characters", ErrValidation, commentBodyMaxRunes)
	}
	return body, nil
}

func mapCommentError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
