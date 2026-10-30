package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// deviceKeyRepository stores per-device public keys and the workspace keys
// wrapped for each device.
//
// Raw SQL is used throughout: the write paths must re-check membership for
// every recipient in the same transaction that stores the envelopes.
type deviceKeyRepository struct {
	db *gorm.DB
}

// NewDeviceKeyRepository returns the Postgres-backed device key repository.
func NewDeviceKeyRepository(db *gorm.DB) domainrepo.DeviceKeyRepository {
	return &deviceKeyRepository{db: db}
}

func (r *deviceKeyRepository) UpsertDeviceKey(ctx context.Context, key *entity.DeviceKey) error {
	var row deviceKeyRow
	err := r.db.WithContext(ctx).Raw(
		`INSERT INTO device_keys (id, user_id, public_key, label, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, public_key) DO UPDATE
		    SET label = EXCLUDED.label,
		        revoked_at = NULL
		 RETURNING id, user_id, public_key, label, created_at, revoked_at`,
		key.ID, key.UserID, key.PublicKey, key.Label, key.CreatedAt,
	).Scan(&row).Error
	if err != nil {
		return fmt.Errorf("upsert device key: %w", err)
	}
	*key = row.toEntity()
	return nil
}

func (r *deviceKeyRepository) ListDeviceKeys(ctx context.Context, userID uuid.UUID) ([]entity.DeviceKey, error) {
	var rows []deviceKeyRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, user_id, public_key, label, created_at, revoked_at
		   FROM device_keys
		  WHERE user_id = ?
		  ORDER BY created_at DESC, id ASC`,
		userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list device keys: %w", err)
	}

	out := make([]entity.DeviceKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toEntity())
	}
	return out, nil
}

func (r *deviceKeyRepository) RevokeDeviceKey(ctx context.Context, id, userID uuid.UUID) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE device_keys
		    SET revoked_at = COALESCE(revoked_at, now())
		  WHERE id = ? AND user_id = ?`,
		id, userID,
	)
	if res.Error != nil {
		return fmt.Errorf("revoke device key: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domainrepo.ErrDeviceKeyNotFound
	}
	return nil
}

func (r *deviceKeyRepository) ListWorkspaceDeviceKeys(ctx context.Context, workspaceID string) ([]entity.WorkspaceDeviceKey, error) {
	var rows []workspaceDeviceKeyRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT dk.id, dk.user_id, dk.public_key, dk.label
		   FROM device_keys dk
		   JOIN workspace_members m
		     ON m.user_id = dk.user_id
		    AND m.workspace_id = ?
		  WHERE dk.revoked_at IS NULL
		  ORDER BY dk.user_id ASC, dk.created_at ASC, dk.id ASC`,
		workspaceID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list workspace device keys: %w", err)
	}

	out := make([]entity.WorkspaceDeviceKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, entity.WorkspaceDeviceKey{
			ID:        row.ID,
			UserID:    row.UserID,
			PublicKey: row.PublicKey,
			Label:     row.Label,
		})
	}
	return out, nil
}

func (r *deviceKeyRepository) UpsertWorkspaceKeys(ctx context.Context, version *entity.WorkspaceKeyVersion, entries []entity.WorkspaceKey) error {
	now := time.Now().UTC()
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, entry := range entries {
			if err := ensureDeviceKeyRecipient(tx, version.WorkspaceID, entry.DeviceKeyID); err != nil {
				return err
			}
		}

		// Create the version row on first upload; a retry with the same
		// version keeps the original creator/timestamp.
		if err := tx.Exec(
			`INSERT INTO workspace_key_versions (workspace_id, version, created_by, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (workspace_id, version) DO NOTHING`,
			version.WorkspaceID, version.Version, version.CreatedBy, version.CreatedAt,
		).Error; err != nil {
			return fmt.Errorf("insert workspace key version: %w", err)
		}

		var row workspaceKeyVersionRow
		err := tx.Raw(
			`SELECT workspace_id, version, created_by, created_at
			   FROM workspace_key_versions
			  WHERE workspace_id = ? AND version = ?`,
			version.WorkspaceID, version.Version,
		).Scan(&row).Error
		if err != nil {
			return fmt.Errorf("load workspace key version: %w", err)
		}
		*version = row.toEntity()

		for _, entry := range entries {
			if err := tx.Exec(
				`INSERT INTO workspace_keys (workspace_id, device_key_id, version, wrapped_key, created_by, created_at)
				 VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT (workspace_id, device_key_id, version) DO UPDATE
				    SET wrapped_key = EXCLUDED.wrapped_key,
				        created_by = EXCLUDED.created_by,
				        created_at = EXCLUDED.created_at`,
				version.WorkspaceID, entry.DeviceKeyID, version.Version, entry.WrappedKey,
				version.CreatedBy, now,
			).Error; err != nil {
				return fmt.Errorf("upsert workspace key: %w", err)
			}
		}
		return nil
	})
}

// ensureDeviceKeyRecipient rejects a recipient that is not an active device
// key of a current workspace member.
func ensureDeviceKeyRecipient(tx *gorm.DB, workspaceID string, deviceKeyID uuid.UUID) error {
	var count int
	err := tx.Raw(
		`SELECT count(*)
		   FROM device_keys dk
		   JOIN workspace_members m
		     ON m.user_id = dk.user_id
		    AND m.workspace_id = ?
		  WHERE dk.id = ? AND dk.revoked_at IS NULL`,
		workspaceID, deviceKeyID,
	).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("check device key recipient: %w", err)
	}
	if count == 0 {
		return domainrepo.ErrDeviceKeyNotMember
	}
	return nil
}

func (r *deviceKeyRepository) ListWorkspaceKeysForUser(ctx context.Context, workspaceID string, userID uuid.UUID) ([]entity.WorkspaceKey, error) {
	var rows []workspaceKeyRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT wk.workspace_id, wk.device_key_id, wk.version, wk.wrapped_key, wk.created_by, wk.created_at
		   FROM workspace_keys wk
		   JOIN device_keys dk ON dk.id = wk.device_key_id
		  WHERE wk.workspace_id = ? AND dk.user_id = ? AND dk.revoked_at IS NULL
		  ORDER BY wk.version DESC, dk.created_at ASC, dk.id ASC`,
		workspaceID, userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list workspace keys: %w", err)
	}

	out := make([]entity.WorkspaceKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toEntity())
	}
	return out, nil
}

func (r *deviceKeyRepository) RotateWorkspaceKey(ctx context.Context, version *entity.WorkspaceKeyVersion) error {
	now := time.Now().UTC()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Concurrent rotations may compute the same next version; the unique
		// PK makes exactly one of them win and the loser reports a conflict.
		var next int
		err := tx.Raw(
			`SELECT COALESCE(MAX(version), 0) + 1
			   FROM workspace_key_versions
			  WHERE workspace_id = ?`,
			version.WorkspaceID,
		).Scan(&next).Error
		if err != nil {
			return fmt.Errorf("next workspace key version: %w", err)
		}

		res := tx.Exec(
			`INSERT INTO workspace_key_versions (workspace_id, version, created_by, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (workspace_id, version) DO NOTHING`,
			version.WorkspaceID, next, version.CreatedBy, now,
		)
		if res.Error != nil {
			return fmt.Errorf("insert rotated workspace key version: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return domainrepo.ErrKeyVersionExists
		}

		version.Version = next
		version.CreatedAt = now
		return nil
	})
}

type deviceKeyRow struct {
	ID        uuid.UUID  `gorm:"column:id"`
	UserID    uuid.UUID  `gorm:"column:user_id"`
	PublicKey string     `gorm:"column:public_key"`
	Label     string     `gorm:"column:label"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
}

func (r deviceKeyRow) toEntity() entity.DeviceKey {
	return entity.DeviceKey{
		ID:        r.ID,
		UserID:    r.UserID,
		PublicKey: r.PublicKey,
		Label:     r.Label,
		CreatedAt: r.CreatedAt,
		RevokedAt: r.RevokedAt,
	}
}

type workspaceDeviceKeyRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	UserID    uuid.UUID `gorm:"column:user_id"`
	PublicKey string    `gorm:"column:public_key"`
	Label     string    `gorm:"column:label"`
}

type workspaceKeyVersionRow struct {
	WorkspaceID string    `gorm:"column:workspace_id"`
	Version     int       `gorm:"column:version"`
	CreatedBy   uuid.UUID `gorm:"column:created_by"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (r workspaceKeyVersionRow) toEntity() entity.WorkspaceKeyVersion {
	return entity.WorkspaceKeyVersion{
		WorkspaceID: r.WorkspaceID,
		Version:     r.Version,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt,
	}
}

type workspaceKeyRow struct {
	WorkspaceID string    `gorm:"column:workspace_id"`
	DeviceKeyID uuid.UUID `gorm:"column:device_key_id"`
	Version     int       `gorm:"column:version"`
	WrappedKey  []byte    `gorm:"column:wrapped_key"`
	CreatedBy   uuid.UUID `gorm:"column:created_by"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (r workspaceKeyRow) toEntity() entity.WorkspaceKey {
	return entity.WorkspaceKey{
		WorkspaceID: r.WorkspaceID,
		DeviceKeyID: r.DeviceKeyID,
		Version:     r.Version,
		WrappedKey:  r.WrappedKey,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt,
	}
}
