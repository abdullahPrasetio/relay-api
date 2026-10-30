package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

// Audit action names. Kept as constants so API consumers can rely on stable
// values and typos are caught at compile time.
const (
	AuditWorkspaceCreate  = "workspace.create"
	AuditWorkspaceRename  = "workspace.rename"
	AuditWorkspaceDelete  = "workspace.delete"
	AuditMemberAdd        = "member.add"
	AuditMemberRoleChange = "member.role_change"
	AuditMemberRemove     = "member.remove"
	AuditInviteCreate     = "invite.create"
	AuditInviteAccept     = "invite.accept"
)

// insertAudit appends one audit event. Callers pass their transaction so the
// event commits (or rolls back) with the mutation it describes.
func insertAudit(tx *gorm.DB, event *entity.AuditEvent) error {
	if len(event.Meta) == 0 {
		event.Meta = json.RawMessage(`{}`)
	}
	event.CreatedAt = time.Now().UTC()
	err := tx.Exec(
		`INSERT INTO audit_events (workspace_id, actor_id, action, target, meta, created_at)
		 VALUES (?, ?, ?, ?, ?::jsonb, ?)`,
		event.WorkspaceID, event.ActorID, event.Action, event.Target, []byte(event.Meta), event.CreatedAt,
	).Error
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func (r *workspaceRepository) ListAudit(ctx context.Context, workspaceID string, beforeID int64, limit int) ([]entity.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if beforeID < 0 {
		beforeID = 0
	}

	var rows []auditRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, workspace_id, actor_id, action, target, meta, created_at
		   FROM audit_events
		  WHERE workspace_id = ? AND (? = 0 OR id < ?)
		  ORDER BY id DESC
		  LIMIT ?`,
		workspaceID, beforeID, beforeID, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}

	out := make([]entity.AuditEvent, 0, len(rows))
	for _, row := range rows {
		meta := json.RawMessage(row.Meta)
		if len(meta) == 0 {
			meta = json.RawMessage(`{}`)
		}
		out = append(out, entity.AuditEvent{
			ID:          row.ID,
			WorkspaceID: row.WorkspaceID,
			ActorID:     row.ActorID,
			Action:      row.Action,
			Target:      row.Target,
			Meta:        meta,
			CreatedAt:   row.CreatedAt,
		})
	}
	return out, nil
}

type auditRow struct {
	ID          int64     `gorm:"column:id"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	ActorID     uuid.UUID `gorm:"column:actor_id"`
	Action      string    `gorm:"column:action"`
	Target      string    `gorm:"column:target"`
	Meta        []byte    `gorm:"column:meta"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}
