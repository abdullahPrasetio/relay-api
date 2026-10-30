package entity

import (
	"time"

	"github.com/google/uuid"
)

// DeviceKey is a per-device X25519 public key registered by a user. The
// private key never leaves the device; the server only stores the public half
// so other members can wrap workspace keys for it.
//
// PublicKey is base64 (standard encoding) of the 32-byte X25519 public key.
// RevokedAt is set when the user revokes the device; revoked keys are hidden
// from the workspace recipient list but remain readable in the owner's list.
type DeviceKey struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"userId"`
	PublicKey string     `json:"publicKey"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

// WorkspaceDeviceKey is an active device key owned by a workspace member. It
// is the recipient list a client needs to wrap a workspace key for everyone.
type WorkspaceDeviceKey struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"userId"`
	PublicKey string    `json:"publicKey"`
	Label     string    `json:"label"`
}

// WorkspaceKeyVersion is one generation of the workspace key. Version numbers
// start at 1 and are never reused.
type WorkspaceKeyVersion struct {
	WorkspaceID string    `json:"workspaceId"`
	Version     int       `json:"version"`
	CreatedBy   uuid.UUID `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

// WorkspaceKey is the workspace key wrapped for one device. WrappedKey is the
// raw 92-byte envelope (ephemeral public key || nonce || ciphertext+tag);
// transport encoding is base64, storage is bytea.
type WorkspaceKey struct {
	WorkspaceID string    `json:"-"`
	DeviceKeyID uuid.UUID `json:"deviceKeyId"`
	Version     int       `json:"version"`
	WrappedKey  []byte    `json:"-"`
	CreatedBy   uuid.UUID `json:"-"`
	CreatedAt   time.Time `json:"-"`
}
