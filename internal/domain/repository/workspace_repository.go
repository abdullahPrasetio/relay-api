package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

var (
	// ErrMemberRequired means the actor is not a member of the workspace (or
	// the workspace does not exist). Callers SHOULD map this to 404 to avoid
	// leaking workspace existence.
	ErrMemberRequired = errors.New("workspace membership required")
	// ErrWriteRequired means the actor is a member but lacks editor privileges.
	ErrWriteRequired = errors.New("workspace editor role required")
	// ErrMemberExists is returned when adding an existing member.
	ErrMemberExists = errors.New("workspace member already exists")
)

// WorkspaceRepository persists workspaces, memberships and entity deltas.
//
// PushChanges and PullChanges re-check membership inside the same query/
// transaction that reads or writes data (plan D8), so a compromised caller
// cannot bypass the usecase-level permission check.
type WorkspaceRepository interface {
	CreateWorkspace(ctx context.Context, workspace *entity.Workspace, ownerRole entity.Role) error
	GetWorkspace(ctx context.Context, id string) (*entity.Workspace, error)
	ListWorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]entity.WorkspaceWithRole, error)
	RenameWorkspace(ctx context.Context, id, name string) error
	SoftDeleteWorkspace(ctx context.Context, id string) error

	GetMember(ctx context.Context, workspaceID string, userID uuid.UUID) (*entity.WorkspaceMember, error)
	ListMembers(ctx context.Context, workspaceID string) ([]*entity.WorkspaceMember, error)
	AddMember(ctx context.Context, member *entity.WorkspaceMember) error
	UpdateMemberRole(ctx context.Context, workspaceID string, userID uuid.UUID, role entity.Role) error
	RemoveMember(ctx context.Context, workspaceID string, userID uuid.UUID) error

	PushChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error)
	PullChanges(ctx context.Context, workspaceID string, userID uuid.UUID, since int64, limit int) (*entity.PullResult, error)
}
