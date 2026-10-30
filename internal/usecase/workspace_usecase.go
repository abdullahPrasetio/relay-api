package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

var (
	// ErrForbidden means the caller is a member but the role is insufficient.
	ErrForbidden = errors.New("forbidden")
	// ErrConflict is reserved for workspace-level conflicts.
	ErrConflict = errors.New("conflict")
	// ErrValidation means the request body/role/kind is invalid.
	ErrValidation = errors.New("validation failed")
	// ErrAlreadyMember means the target user is already in the workspace.
	ErrAlreadyMember = errors.New("already a member")
)

// WorkspaceUseCase is the workspace CRUD, membership and delta-sync service.
//
// Permissions follow the plan matrix (entity.Role.Allows): owner can do
// everything, admin can do everything except delete the workspace, editor can
// push/pull, viewer can only pull.
type WorkspaceUseCase interface {
	CreateWorkspace(ctx context.Context, ownerID uuid.UUID, name string) (*entity.Workspace, error)
	ListWorkspaces(ctx context.Context, userID uuid.UUID) ([]entity.WorkspaceWithRole, error)
	GetWorkspace(ctx context.Context, userID uuid.UUID, workspaceID string) (*entity.WorkspaceWithRole, error)
	RenameWorkspace(ctx context.Context, userID uuid.UUID, workspaceID, name string) (*entity.Workspace, error)
	DeleteWorkspace(ctx context.Context, userID uuid.UUID, workspaceID string) error

	ListMembers(ctx context.Context, userID uuid.UUID, workspaceID string) ([]*entity.WorkspaceMember, error)
	AddMember(ctx context.Context, actorID uuid.UUID, workspaceID string, userID uuid.UUID, role entity.Role) (*entity.WorkspaceMember, error)
	UpdateMemberRole(ctx context.Context, actorID uuid.UUID, workspaceID string, userID uuid.UUID, role entity.Role) (*entity.WorkspaceMember, error)
	RemoveMember(ctx context.Context, actorID uuid.UUID, workspaceID string, userID uuid.UUID) error

	// ListAudit returns audit events newest-first. Only admin+ may read them.
	// nextBefore is 0 when there are no older events.
	ListAudit(ctx context.Context, userID uuid.UUID, workspaceID string, beforeID int64, limit int) ([]entity.AuditEvent, int64, error)

	PushChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error)
	PullChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, since int64, limit int) (*entity.PullResult, error)
}

type workspaceUseCase struct {
	repo domainrepo.WorkspaceRepository
}

// NewWorkspaceUseCase wires the workspace repository into the usecase.
func NewWorkspaceUseCase(repo domainrepo.WorkspaceRepository) WorkspaceUseCase {
	return &workspaceUseCase{repo: repo}
}

func (u *workspaceUseCase) CreateWorkspace(ctx context.Context, ownerID uuid.UUID, name string) (*entity.Workspace, error) {
	name, err := validWorkspaceName(name)
	if err != nil {
		return nil, err
	}
	if ownerID == uuid.Nil {
		return nil, fmt.Errorf("%w: owner id is required", ErrValidation)
	}

	id, err := newWorkspaceID()
	if err != nil {
		return nil, err
	}

	workspace := &entity.Workspace{ID: id, Name: name, OwnerID: ownerID}
	if err := u.repo.CreateWorkspace(ctx, workspace, entity.RoleOwner); err != nil {
		return nil, err
	}
	return workspace, nil
}

func (u *workspaceUseCase) ListWorkspaces(ctx context.Context, userID uuid.UUID) ([]entity.WorkspaceWithRole, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: user id is required", ErrValidation)
	}
	return u.repo.ListWorkspacesForUser(ctx, userID)
}

func (u *workspaceUseCase) GetWorkspace(ctx context.Context, userID uuid.UUID, workspaceID string) (*entity.WorkspaceWithRole, error) {
	workspace, role, err := u.authorize(ctx, workspaceID, userID, entity.PermissionPull)
	if err != nil {
		return nil, err
	}
	return &entity.WorkspaceWithRole{Workspace: *workspace, Role: role}, nil
}

func (u *workspaceUseCase) RenameWorkspace(ctx context.Context, userID uuid.UUID, workspaceID, name string) (*entity.Workspace, error) {
	name, err := validWorkspaceName(name)
	if err != nil {
		return nil, err
	}
	workspace, _, err := u.authorize(ctx, workspaceID, userID, entity.PermissionUpdateWorkspace)
	if err != nil {
		return nil, err
	}
	if err := u.repo.RenameWorkspace(ctx, workspaceID, name, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	workspace.Name = name
	return workspace, nil
}

func (u *workspaceUseCase) DeleteWorkspace(ctx context.Context, userID uuid.UUID, workspaceID string) error {
	if _, _, err := u.authorize(ctx, workspaceID, userID, entity.PermissionDeleteWorkspace); err != nil {
		return err
	}
	if err := u.repo.SoftDeleteWorkspace(ctx, workspaceID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (u *workspaceUseCase) ListMembers(ctx context.Context, userID uuid.UUID, workspaceID string) ([]*entity.WorkspaceMember, error) {
	if _, _, err := u.authorize(ctx, workspaceID, userID, entity.PermissionListMembers); err != nil {
		return nil, err
	}
	return u.repo.ListMembers(ctx, workspaceID)
}

func (u *workspaceUseCase) AddMember(ctx context.Context, actorID uuid.UUID, workspaceID string, userID uuid.UUID, role entity.Role) (*entity.WorkspaceMember, error) {
	if _, actorRole, err := u.authorize(ctx, workspaceID, actorID, entity.PermissionInvite); err != nil {
		return nil, err
	} else if err := validateInvitableRole(role, actorRole); err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: user id is required", ErrValidation)
	}

	invitedBy := actorID
	member := &entity.WorkspaceMember{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        role,
		InvitedBy:   &invitedBy,
	}
	if err := u.repo.AddMember(ctx, member); err != nil {
		if errors.Is(err, domainrepo.ErrMemberExists) {
			return nil, ErrAlreadyMember
		}
		return nil, err
	}
	return member, nil
}

func (u *workspaceUseCase) UpdateMemberRole(ctx context.Context, actorID uuid.UUID, workspaceID string, userID uuid.UUID, role entity.Role) (*entity.WorkspaceMember, error) {
	_, actorRole, err := u.authorize(ctx, workspaceID, actorID, entity.PermissionManageMembers)
	if err != nil {
		return nil, err
	}
	if err := validateInvitableRole(role, actorRole); err != nil {
		return nil, err
	}

	target, err := u.repo.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if target.Role == entity.RoleOwner {
		return nil, fmt.Errorf("%w: the workspace owner role cannot be changed", ErrForbidden)
	}
	if target.Role == entity.RoleAdmin && actorRole != entity.RoleOwner {
		return nil, fmt.Errorf("%w: only the owner can change an admin role", ErrForbidden)
	}

	if err := u.repo.UpdateMemberRole(ctx, workspaceID, userID, role, actorID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	target.Role = role
	return target, nil
}

func (u *workspaceUseCase) RemoveMember(ctx context.Context, actorID uuid.UUID, workspaceID string, userID uuid.UUID) error {
	_, actorRole, err := u.authorize(ctx, workspaceID, actorID, entity.PermissionManageMembers)
	if err != nil {
		return err
	}

	target, err := u.repo.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if target.Role == entity.RoleOwner {
		return fmt.Errorf("%w: the workspace owner cannot be removed", ErrForbidden)
	}
	if target.Role == entity.RoleAdmin && actorRole != entity.RoleOwner {
		return fmt.Errorf("%w: only the owner can remove an admin", ErrForbidden)
	}

	if err := u.repo.RemoveMember(ctx, workspaceID, userID, actorID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// ListAudit pages the workspace audit log. Admin+ only: the audit log exposes
// actor identities and security-relevant actions.
func (u *workspaceUseCase) ListAudit(ctx context.Context, userID uuid.UUID, workspaceID string, beforeID int64, limit int) ([]entity.AuditEvent, int64, error) {
	if _, _, err := u.authorize(ctx, workspaceID, userID, entity.PermissionManageMembers); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	events, err := u.repo.ListAudit(ctx, workspaceID, beforeID, limit)
	if err != nil {
		return nil, 0, err
	}

	var nextBefore int64
	if len(events) == limit && len(events) > 0 {
		nextBefore = events[len(events)-1].ID
	}
	return events, nextBefore, nil
}

// PushChanges applies a client delta batch. The repository re-checks
// editor+ membership in the same transaction and dedupes by (workspace, opId).
func (u *workspaceUseCase) PushChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, changes []entity.Change) ([]entity.PushResult, int64, error) {
	if workspaceID == "" || actorID == uuid.Nil {
		return nil, 0, fmt.Errorf("%w: workspace id and actor id are required", ErrValidation)
	}
	for i, change := range changes {
		if err := validateChange(change); err != nil {
			return nil, 0, fmt.Errorf("change %d: %w", i, err)
		}
	}

	results, serverSeq, err := u.repo.PushChanges(ctx, workspaceID, actorID, changes)
	if err != nil {
		return nil, 0, mapSyncError(err)
	}
	return results, serverSeq, nil
}

// PullChanges returns the delta page after since. Viewer+ membership is
// required.
func (u *workspaceUseCase) PullChanges(ctx context.Context, workspaceID string, actorID uuid.UUID, since int64, limit int) (*entity.PullResult, error) {
	if workspaceID == "" || actorID == uuid.Nil {
		return nil, fmt.Errorf("%w: workspace id and actor id are required", ErrValidation)
	}
	if since < 0 {
		since = 0
	}

	result, err := u.repo.PullChanges(ctx, workspaceID, actorID, since, limit)
	if err != nil {
		return nil, mapSyncError(err)
	}
	return result, nil
}

func (u *workspaceUseCase) authorize(ctx context.Context, workspaceID string, userID uuid.UUID, permission entity.Permission) (*entity.Workspace, entity.Role, error) {
	if workspaceID == "" {
		return nil, "", fmt.Errorf("%w: workspace id is required", ErrValidation)
	}
	if userID == uuid.Nil {
		return nil, "", fmt.Errorf("%w: user id is required", ErrValidation)
	}

	member, err := u.repo.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Not a member and not a known workspace are indistinguishable to
			// the caller (plan D8): always 404, never 403.
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	if !member.Role.Allows(permission) {
		return nil, "", fmt.Errorf("%w: role %q cannot %s", ErrForbidden, member.Role, permission)
	}

	workspace, err := u.repo.GetWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	return workspace, member.Role, nil
}

func validateChange(change entity.Change) error {
	if strings.TrimSpace(change.OpID) == "" {
		return fmt.Errorf("%w: opId is required", ErrValidation)
	}
	if strings.TrimSpace(change.ID) == "" {
		return fmt.Errorf("%w: entity id is required", ErrValidation)
	}
	if change.BaseRev < 0 {
		return fmt.Errorf("%w: baseRev must be >= 0", ErrValidation)
	}
	if change.Kind != "" && !entity.ValidKind(change.Kind) {
		return fmt.Errorf("%w: unknown entity kind %q", ErrValidation, change.Kind)
	}
	if change.BaseRev == 0 && change.Deleted {
		return fmt.Errorf("%w: cannot delete an entity with baseRev 0", ErrValidation)
	}
	if change.BaseRev == 0 && !change.Deleted && !entity.ValidKind(change.Kind) {
		return fmt.Errorf("%w: kind is required when creating an entity", ErrValidation)
	}
	if len(change.Payload) > 0 && !json.Valid(change.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrValidation)
	}
	return nil
}

func validateInvitableRole(role entity.Role, actorRole entity.Role) error {
	switch role {
	case entity.RoleAdmin, entity.RoleEditor, entity.RoleViewer:
	default:
		return fmt.Errorf("%w: role must be admin, editor or viewer", ErrValidation)
	}
	// Only the owner may grant admin; admins can manage editors/viewers.
	if role == entity.RoleAdmin && actorRole != entity.RoleOwner {
		return fmt.Errorf("%w: only the owner can grant the admin role", ErrForbidden)
	}
	return nil
}

func validWorkspaceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrValidation)
	}
	if len([]rune(name)) > 120 {
		return "", fmt.Errorf("%w: name must be at most 120 characters", ErrValidation)
	}
	return name, nil
}

func mapSyncError(err error) error {
	switch {
	case errors.Is(err, domainrepo.ErrMemberRequired):
		return ErrNotFound
	case errors.Is(err, domainrepo.ErrWriteRequired):
		return ErrForbidden
	default:
		return err
	}
}

// newWorkspaceID returns an opaque `ws_` identifier matching IDs created by
// the desktop app. The ID is not a UUID by design.
func newWorkspaceID() (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate workspace id: %w", err)
	}
	return "ws_" + hex.EncodeToString(buf[:]), nil
}
