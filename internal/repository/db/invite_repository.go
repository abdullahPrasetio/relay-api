package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

type inviteRepository struct {
	db *gorm.DB
}

// NewInviteRepository returns the Postgres-backed invite repository.
func NewInviteRepository(db *gorm.DB) domainrepo.InviteRepository {
	return &inviteRepository{db: db}
}

func (r *inviteRepository) CreateInvite(ctx context.Context, invite *entity.Invite) error {
	now := time.Now().UTC()
	invite.CreatedAt = now

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO invites (id, workspace_id, role, token_hash, created_by, expires_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			invite.ID, invite.WorkspaceID, string(invite.Role), invite.TokenHash,
			invite.CreatedBy, invite.ExpiresAt, now,
		).Error; err != nil {
			return fmt.Errorf("insert invite: %w", err)
		}

		meta, err := json.Marshal(map[string]string{
			"role":      string(invite.Role),
			"expiresAt": invite.ExpiresAt.UTC().Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("encode invite audit meta: %w", err)
		}
		return insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: invite.WorkspaceID,
			ActorID:     invite.CreatedBy,
			Action:      AuditInviteCreate,
			Target:      invite.ID.String(),
			Meta:        meta,
		})
	})
}

func (r *inviteRepository) GetInviteInfo(ctx context.Context, tokenHash string) (*entity.InviteInfo, error) {
	var row inviteInfoRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT i.workspace_id, i.role, i.expires_at, i.accepted_at, w.name AS workspace_name
		   FROM invites i
		   JOIN workspaces w ON w.id = i.workspace_id
		  WHERE i.token_hash = ? AND w.deleted_at IS NULL`,
		tokenHash,
	).Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get invite: %w", err)
	}
	if row.WorkspaceID == "" {
		return nil, domainrepo.ErrInviteNotFound
	}
	if row.AcceptedAt.Valid {
		return nil, domainrepo.ErrInviteUsed
	}
	if !row.ExpiresAt.After(time.Now().UTC()) {
		return nil, domainrepo.ErrInviteExpired
	}

	return &entity.InviteInfo{
		WorkspaceID:   row.WorkspaceID,
		WorkspaceName: row.WorkspaceName,
		Role:          entity.Role(row.Role),
		ExpiresAt:     row.ExpiresAt.UTC(),
	}, nil
}

func (r *inviteRepository) AcceptInvite(ctx context.Context, tokenHash string, userID uuid.UUID) (*entity.Workspace, entity.Role, error) {
	var (
		workspace entity.Workspace
		role      entity.Role
	)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row inviteAcceptRow
		if err := tx.Raw(
			`SELECT i.id, i.workspace_id, i.role, i.created_by, i.expires_at, i.accepted_at,
			        w.name AS workspace_name, w.owner_id, w.created_at AS workspace_created_at
			   FROM invites i
			   JOIN workspaces w ON w.id = i.workspace_id
			  WHERE i.token_hash = ? AND w.deleted_at IS NULL
			  FOR UPDATE OF i`,
			tokenHash,
		).Scan(&row).Error; err != nil {
			return fmt.Errorf("lock invite: %w", err)
		}
		if row.ID == uuid.Nil {
			return domainrepo.ErrInviteNotFound
		}
		if row.AcceptedAt.Valid {
			return domainrepo.ErrInviteUsed
		}
		if !row.ExpiresAt.After(time.Now().UTC()) {
			return domainrepo.ErrInviteExpired
		}

		var existing int64
		if err := tx.Raw(
			`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
			row.WorkspaceID, userID,
		).Row().Scan(&existing); err != nil {
			return fmt.Errorf("check membership: %w", err)
		}
		if existing > 0 {
			return domainrepo.ErrInviteAlreadyMember
		}

		now := time.Now().UTC()
		if err := tx.Exec(
			`INSERT INTO workspace_members (workspace_id, user_id, role, invited_by, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			row.WorkspaceID, userID, row.Role, row.CreatedBy, now,
		).Error; err != nil {
			return fmt.Errorf("insert accepted member: %w", err)
		}

		res := tx.Exec(
			`UPDATE invites SET accepted_at = ?, accepted_by = ? WHERE id = ? AND accepted_at IS NULL`,
			now, userID, row.ID,
		)
		if res.Error != nil {
			return fmt.Errorf("mark invite accepted: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			// Another transaction consumed the invite between the lock and the
			// update; the transaction rolls back the membership insert.
			return domainrepo.ErrInviteUsed
		}

		meta, err := json.Marshal(map[string]string{
			"inviteId": row.ID.String(),
			"role":     row.Role,
		})
		if err != nil {
			return fmt.Errorf("encode invite audit meta: %w", err)
		}
		if err := insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: row.WorkspaceID,
			ActorID:     userID,
			Action:      AuditInviteAccept,
			Target:      row.ID.String(),
			Meta:        meta,
		}); err != nil {
			return err
		}

		workspace = entity.Workspace{
			ID:        row.WorkspaceID,
			Name:      row.WorkspaceName,
			OwnerID:   row.OwnerID,
			CreatedAt: row.WorkspaceCreatedAt,
		}
		role = entity.Role(row.Role)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return &workspace, role, nil
}

type inviteInfoRow struct {
	WorkspaceID   string       `gorm:"column:workspace_id"`
	WorkspaceName string       `gorm:"column:workspace_name"`
	Role          string       `gorm:"column:role"`
	ExpiresAt     time.Time    `gorm:"column:expires_at"`
	AcceptedAt    sql.NullTime `gorm:"column:accepted_at"`
}

type inviteAcceptRow struct {
	ID                 uuid.UUID    `gorm:"column:id"`
	WorkspaceID        string       `gorm:"column:workspace_id"`
	WorkspaceName      string       `gorm:"column:workspace_name"`
	Role               string       `gorm:"column:role"`
	CreatedBy          uuid.UUID    `gorm:"column:created_by"`
	ExpiresAt          time.Time    `gorm:"column:expires_at"`
	AcceptedAt         sql.NullTime `gorm:"column:accepted_at"`
	OwnerID            uuid.UUID    `gorm:"column:owner_id"`
	WorkspaceCreatedAt time.Time    `gorm:"column:workspace_created_at"`
}
