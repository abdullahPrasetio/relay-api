package entity

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Role is a workspace member role. The ordering matters for permission checks:
// owner > admin > editor > viewer.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Valid reports whether r is one of the four known roles.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleEditor, RoleViewer:
		return true
	default:
		return false
	}
}

// Permission is an action guarded by the workspace role matrix.
type Permission string

const (
	PermissionPull            Permission = "pull"
	PermissionPush            Permission = "push"
	PermissionInvite          Permission = "invite"
	PermissionManageMembers   Permission = "manage_members"
	PermissionListMembers     Permission = "list_members"
	PermissionUpdateWorkspace Permission = "update_workspace"
	PermissionDeleteWorkspace Permission = "delete_workspace"
)

// Allows reports whether the role may perform p.
//
// owner  : everything
// admin  : everything except deleting the workspace (owner-only per the plan)
// editor : pull, push, list members
// viewer : pull, list members
func (r Role) Allows(p Permission) bool {
	switch r {
	case RoleOwner:
		return true
	case RoleAdmin:
		switch p {
		case PermissionPull, PermissionPush, PermissionInvite,
			PermissionManageMembers, PermissionListMembers, PermissionUpdateWorkspace:
			return true
		default:
			return false
		}
	case RoleEditor:
		switch p {
		case PermissionPull, PermissionPush, PermissionListMembers:
			return true
		default:
			return false
		}
	case RoleViewer:
		switch p {
		case PermissionPull, PermissionListMembers:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// Workspace is a team workspace. IDs are opaque text from the desktop app.
type Workspace struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	OwnerID   uuid.UUID  `json:"ownerId"`
	CreatedAt time.Time  `json:"createdAt"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
}

// WorkspaceWithRole is a workspace plus the caller's role in it.
type WorkspaceWithRole struct {
	Workspace
	Role Role `json:"role"`
}

// WorkspaceMember is a membership row.
type WorkspaceMember struct {
	WorkspaceID string     `json:"workspaceId"`
	UserID      uuid.UUID  `json:"userId"`
	Role        Role       `json:"role"`
	InvitedBy   *uuid.UUID `json:"invitedBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// Entity is a synced workspace node (collection/request/environment/globals).
type Entity struct {
	WorkspaceID string          `json:"workspaceId"`
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	ParentID    *string         `json:"parentId,omitempty"`
	OrderKey    *string         `json:"orderKey,omitempty"`
	Rev         int64           `json:"rev"`
	Payload     json.RawMessage `json:"payload"`
	DeletedAt   *time.Time      `json:"deletedAt,omitempty"`
	UpdatedBy   uuid.UUID       `json:"updatedBy"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// Entity kind values accepted by the schema.
const (
	KindCollection  = "collection"
	KindFolder      = "folder"
	KindRequest     = "request"
	KindEnvironment = "environment"
	KindGlobals     = "globals"
)

// ValidKind reports whether kind is allowed by the entities table check.
func ValidKind(kind string) bool {
	switch kind {
	case KindCollection, KindFolder, KindRequest, KindEnvironment, KindGlobals:
		return true
	default:
		return false
	}
}

// Change is one client push operation.
type Change struct {
	OpID     string          `json:"opId"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind,omitempty"`
	BaseRev  int64           `json:"baseRev"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	ParentID *string         `json:"parentId,omitempty"`
	OrderKey *string         `json:"orderKey,omitempty"`
	Deleted  bool            `json:"deleted,omitempty"`
}

// ServerState is the authoritative entity state returned on conflict.
type ServerState struct {
	Rev     int64           `json:"rev"`
	Payload json.RawMessage `json:"payload"`
	Deleted bool            `json:"deleted"`
}

// PushResult is the per-change outcome. Stored verbatim in entity_ops so a
// retried opId returns exactly the same response.
type PushResult struct {
	OpID     string       `json:"opId"`
	Applied  bool         `json:"applied"`
	Conflict bool         `json:"conflict,omitempty"`
	Server   *ServerState `json:"server,omitempty"`
	Reason   string       `json:"reason,omitempty"`
}

// PulledChange is one entity_changes row enriched with the current entity.
type PulledChange struct {
	Seq      int64           `json:"seq"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Rev      int64           `json:"rev"`
	Deleted  bool            `json:"deleted"`
	Payload  json.RawMessage `json:"payload"`
	ParentID *string         `json:"parentId,omitempty"`
	OrderKey *string         `json:"orderKey,omitempty"`
}

// PullResult is a delta page. NextSeq is the cursor to send on the next pull;
// HasMore means at least one more change exists after this page.
type PullResult struct {
	Changes []PulledChange `json:"changes"`
	NextSeq int64          `json:"nextSeq"`
	HasMore bool           `json:"hasMore"`
}
