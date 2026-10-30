package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

var (
	// ErrDeviceKeyNotFound means the device key does not exist or is not owned
	// by the caller. Callers SHOULD map this to 404 so ownership is not leaked.
	ErrDeviceKeyNotFound = errors.New("device key not found")
	// ErrDeviceKeyNotMember means a wrapped-key entry names a device key whose
	// owner is not a member of the workspace, or the key is revoked.
	ErrDeviceKeyNotMember = errors.New("device key does not belong to a workspace member")
	// ErrKeyVersionExists means the workspace key version already exists.
	// Rotation never overwrites an existing version.
	ErrKeyVersionExists = errors.New("workspace key version already exists")
)

// DeviceKeyRepository persists per-device public keys and the workspace keys
// wrapped for each device.
//
// UpsertWorkspaceKeys re-checks membership for every recipient inside the same
// transaction that writes the envelopes, so a stale client cannot wrap a key
// for a user who has left the workspace.
type DeviceKeyRepository interface {
	// UpsertDeviceKey registers key, or reactivates/relabels the row with the
	// same (user_id, public_key). The stored row is written back to key.
	UpsertDeviceKey(ctx context.Context, key *entity.DeviceKey) error
	ListDeviceKeys(ctx context.Context, userID uuid.UUID) ([]entity.DeviceKey, error)
	// RevokeDeviceKey marks an owned key revoked and is idempotent.
	RevokeDeviceKey(ctx context.Context, id, userID uuid.UUID) error

	// ListWorkspaceDeviceKeys returns the active device keys of every member of
	// the workspace, ordered deterministically.
	ListWorkspaceDeviceKeys(ctx context.Context, workspaceID string) ([]entity.WorkspaceDeviceKey, error)

	// UpsertWorkspaceKeys stores version (creating it when missing) and upserts
	// the per-device envelopes in one transaction. The actual version row is
	// written back to version.
	UpsertWorkspaceKeys(ctx context.Context, version *entity.WorkspaceKeyVersion, entries []entity.WorkspaceKey) error
	// ListWorkspaceKeysForUser returns only the envelopes wrapped for the
	// caller's own active device keys.
	ListWorkspaceKeysForUser(ctx context.Context, workspaceID string, userID uuid.UUID) ([]entity.WorkspaceKey, error)
	// RotateWorkspaceKey inserts the next version (current max + 1) and writes
	// it back. It returns ErrKeyVersionExists instead of overwriting.
	RotateWorkspaceKey(ctx context.Context, version *entity.WorkspaceKeyVersion) error
}
