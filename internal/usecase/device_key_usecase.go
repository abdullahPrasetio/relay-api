package usecase

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// Envelope sizes shared by the client and this API.
const (
	// X25519 public keys are 32 bytes.
	deviceKeySize = 32
	// Wrapped workspace key: ephemeral public key (32) || AES-GCM nonce (12)
	// || ciphertext of the 32-byte workspace key including the 16-byte tag.
	wrappedKeySize = 32 + 12 + 32 + 16
	// Device labels are short human-readable hints ("MacBook Pro").
	maxDeviceKeyLabel = 120
)

// KeyUseCase is the device key and workspace key registry.
//
// Anyone may register and revoke their own device keys. Workspace key writes
// (uploading wrapped keys, rotating) require admin+; any member may read the
// recipient list and their own wrapped keys.
type KeyUseCase interface {
	RegisterDeviceKey(ctx context.Context, userID uuid.UUID, publicKey, label string) (*entity.DeviceKey, error)
	ListDeviceKeys(ctx context.Context, userID uuid.UUID) ([]entity.DeviceKey, error)
	RevokeDeviceKey(ctx context.Context, userID, keyID uuid.UUID) error

	ListWorkspaceDeviceKeys(ctx context.Context, userID uuid.UUID, workspaceID string) ([]entity.WorkspaceDeviceKey, error)
	PutWorkspaceKeys(ctx context.Context, actorID uuid.UUID, workspaceID string, input PutWorkspaceKeysInput) (*entity.WorkspaceKeyVersion, error)
	ListMyWorkspaceKeys(ctx context.Context, userID uuid.UUID, workspaceID string) ([]entity.WorkspaceKey, error)
	RotateWorkspaceKey(ctx context.Context, actorID uuid.UUID, workspaceID string) (*entity.WorkspaceKeyVersion, error)
}

// PutWorkspaceKeysInput is the validated upload payload: one wrapped workspace
// key per recipient device.
type PutWorkspaceKeysInput struct {
	Version int
	Entries []WrappedKeyInput
}

// WrappedKeyInput is one recipient envelope as sent by the client.
type WrappedKeyInput struct {
	DeviceKeyID uuid.UUID
	WrappedKey  string // base64 of the 92-byte envelope
}

type keyUseCase struct {
	keys    domainrepo.DeviceKeyRepository
	members WorkspaceAuthorizer
}

// NewKeyUseCase wires the device key repository and workspace membership
// lookup into the key usecase.
func NewKeyUseCase(keys domainrepo.DeviceKeyRepository, members WorkspaceAuthorizer) KeyUseCase {
	return &keyUseCase{keys: keys, members: members}
}

func (u *keyUseCase) RegisterDeviceKey(ctx context.Context, userID uuid.UUID, publicKey, label string) (*entity.DeviceKey, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: user id is required", ErrValidation)
	}
	canonical, err := decodeKeyMaterial("publicKey", publicKey, deviceKeySize)
	if err != nil {
		return nil, err
	}
	label, err = validDeviceKeyLabel(label)
	if err != nil {
		return nil, err
	}

	key := &entity.DeviceKey{
		ID:        uuid.New(),
		UserID:    userID,
		PublicKey: canonical,
		Label:     label,
		CreatedAt: time.Now().UTC(),
	}
	if err := u.keys.UpsertDeviceKey(ctx, key); err != nil {
		return nil, err
	}
	return key, nil
}

func (u *keyUseCase) ListDeviceKeys(ctx context.Context, userID uuid.UUID) ([]entity.DeviceKey, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: user id is required", ErrValidation)
	}
	return u.keys.ListDeviceKeys(ctx, userID)
}

func (u *keyUseCase) RevokeDeviceKey(ctx context.Context, userID, keyID uuid.UUID) error {
	if userID == uuid.Nil {
		return fmt.Errorf("%w: user id is required", ErrValidation)
	}
	if keyID == uuid.Nil {
		return fmt.Errorf("%w: device key id is required", ErrValidation)
	}
	if err := u.keys.RevokeDeviceKey(ctx, keyID, userID); err != nil {
		if errors.Is(err, domainrepo.ErrDeviceKeyNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (u *keyUseCase) ListWorkspaceDeviceKeys(ctx context.Context, userID uuid.UUID, workspaceID string) ([]entity.WorkspaceDeviceKey, error) {
	if err := u.authorize(ctx, workspaceID, userID, entity.PermissionListMembers); err != nil {
		return nil, err
	}
	return u.keys.ListWorkspaceDeviceKeys(ctx, workspaceID)
}

func (u *keyUseCase) PutWorkspaceKeys(ctx context.Context, actorID uuid.UUID, workspaceID string, input PutWorkspaceKeysInput) (*entity.WorkspaceKeyVersion, error) {
	if err := u.authorize(ctx, workspaceID, actorID, entity.PermissionManageMembers); err != nil {
		return nil, err
	}
	if input.Version < 1 {
		return nil, fmt.Errorf("%w: version must be >= 1", ErrValidation)
	}
	if len(input.Entries) == 0 {
		return nil, fmt.Errorf("%w: at least one wrapped key entry is required", ErrValidation)
	}

	entries := make([]entity.WorkspaceKey, 0, len(input.Entries))
	seen := make(map[uuid.UUID]struct{}, len(input.Entries))
	for i, entry := range input.Entries {
		if entry.DeviceKeyID == uuid.Nil {
			return nil, fmt.Errorf("%w: entry %d: deviceKeyId is required", ErrValidation, i)
		}
		if _, dup := seen[entry.DeviceKeyID]; dup {
			return nil, fmt.Errorf("%w: entry %d: deviceKeyId %s is duplicated", ErrValidation, i, entry.DeviceKeyID)
		}
		seen[entry.DeviceKeyID] = struct{}{}

		wrapped, err := decodeWrappedKey(entry.WrappedKey)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		entries = append(entries, entity.WorkspaceKey{
			WorkspaceID: workspaceID,
			DeviceKeyID: entry.DeviceKeyID,
			Version:     input.Version,
			WrappedKey:  wrapped,
		})
	}

	version := &entity.WorkspaceKeyVersion{
		WorkspaceID: workspaceID,
		Version:     input.Version,
		CreatedBy:   actorID,
	}
	if err := u.keys.UpsertWorkspaceKeys(ctx, version, entries); err != nil {
		switch {
		case errors.Is(err, domainrepo.ErrDeviceKeyNotMember):
			return nil, fmt.Errorf("%w: every device key must belong to an active device of a workspace member", ErrValidation)
		case errors.Is(err, domainrepo.ErrKeyVersionExists):
			return nil, ErrConflict
		default:
			return nil, err
		}
	}
	return version, nil
}

func (u *keyUseCase) ListMyWorkspaceKeys(ctx context.Context, userID uuid.UUID, workspaceID string) ([]entity.WorkspaceKey, error) {
	if err := u.authorize(ctx, workspaceID, userID, entity.PermissionPull); err != nil {
		return nil, err
	}
	return u.keys.ListWorkspaceKeysForUser(ctx, workspaceID, userID)
}

func (u *keyUseCase) RotateWorkspaceKey(ctx context.Context, actorID uuid.UUID, workspaceID string) (*entity.WorkspaceKeyVersion, error) {
	if err := u.authorize(ctx, workspaceID, actorID, entity.PermissionManageMembers); err != nil {
		return nil, err
	}

	version := &entity.WorkspaceKeyVersion{
		WorkspaceID: workspaceID,
		CreatedBy:   actorID,
	}
	if err := u.keys.RotateWorkspaceKey(ctx, version); err != nil {
		switch {
		case errors.Is(err, domainrepo.ErrKeyVersionExists):
			return nil, ErrConflict
		default:
			return nil, err
		}
	}
	return version, nil
}

// authorize resolves the caller's membership. A missing workspace and a
// workspace the caller cannot see are both ErrNotFound so membership is not
// leaked (same contract as the workspace endpoints).
func (u *keyUseCase) authorize(ctx context.Context, workspaceID string, userID uuid.UUID, permission entity.Permission) error {
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("%w: workspace id is required", ErrValidation)
	}
	if userID == uuid.Nil {
		return fmt.Errorf("%w: user id is required", ErrValidation)
	}

	member, err := u.members.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if !member.Role.Allows(permission) {
		return fmt.Errorf("%w: role %q cannot %s", ErrForbidden, member.Role, permission)
	}
	return nil
}

// decodeKeyMaterial validates a base64 key field and returns its canonical
// standard-encoding form. Standard base64 with or without padding is accepted
// so both the browser (btoa) and Node clients interoperate.
func decodeKeyMaterial(field, value string, size int) (string, error) {
	raw, err := decodeBase64(field, value, size)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func decodeWrappedKey(value string) ([]byte, error) {
	return decodeBase64("wrappedKey", value, wrappedKeySize)
}

func decodeBase64(field, value string, size int) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("%w: %s is required", ErrValidation, field)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be valid base64", ErrValidation, field)
	}
	if len(raw) != size {
		return nil, fmt.Errorf("%w: %s must decode to %d bytes", ErrValidation, field, size)
	}
	return raw, nil
}

func validDeviceKeyLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxDeviceKeyLabel {
		return "", fmt.Errorf("%w: label must be at most %d characters", ErrValidation, maxDeviceKeyLabel)
	}
	return label, nil
}
