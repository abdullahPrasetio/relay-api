package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// commentRepository stores per-entity comments. Mutations append their audit
// event in the same transaction, so an audit failure rolls the change back.
type commentRepository struct {
	db *gorm.DB
}

// NewCommentRepository returns the Postgres-backed comment repository.
func NewCommentRepository(db *gorm.DB) domainrepo.CommentRepository {
	return &commentRepository{db: db}
}

func (r *commentRepository) CreateComment(ctx context.Context, comment *entity.Comment) error {
	now := time.Now().UTC()
	comment.CreatedAt = now
	comment.UpdatedAt = now

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Exec(
			`INSERT INTO comments
			   (id, workspace_id, entity_id, user_id, body, resolved_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			comment.ID, comment.WorkspaceID, comment.EntityID, comment.UserID,
			comment.Body, comment.ResolvedAt, now, now,
		).Error
		if err != nil {
			return fmt.Errorf("insert comment: %w", err)
		}
		return insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: comment.WorkspaceID,
			ActorID:     comment.UserID,
			Action:      domainrepo.CommentActionCreate,
			Target:      comment.ID.String(),
			Meta:        commentAuditMeta(comment, nil),
		})
	})
}

func (r *commentRepository) GetComment(ctx context.Context, id uuid.UUID) (*entity.Comment, error) {
	var row commentRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, workspace_id, entity_id, user_id, body, resolved_at, created_at, updated_at
		   FROM comments WHERE id = ?`,
		id,
	).Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get comment: %w", err)
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}
	return row.toEntity(), nil
}

func (r *commentRepository) ListComments(ctx context.Context, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, error) {
	query := `SELECT id, workspace_id, entity_id, user_id, body, resolved_at, created_at, updated_at
	            FROM comments
	           WHERE workspace_id = ? AND entity_id = ?`
	args := []any{workspaceID, entityID}

	if before != nil {
		var anchor time.Time
		row := r.db.WithContext(ctx).Raw(
			`SELECT created_at FROM comments
			  WHERE id = ? AND workspace_id = ? AND entity_id = ?`,
			*before, workspaceID, entityID,
		).Row()
		if err := row.Scan(&anchor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, domainrepo.ErrCommentCursorNotFound
			}
			return nil, fmt.Errorf("resolve comment cursor: %w", err)
		}
		query += ` AND (created_at, id) < (?::timestamptz, ?::uuid)`
		args = append(args, anchor, *before)
	}

	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	var rows []commentRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	out := make([]entity.Comment, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row.toEntity())
	}
	return out, nil
}

func (r *commentRepository) UpdateComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID, action string) error {
	now := time.Now().UTC()
	comment.UpdatedAt = now

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(
			`UPDATE comments
			    SET body = ?, resolved_at = ?, updated_at = ?
			  WHERE id = ?`,
			comment.Body, comment.ResolvedAt, now, comment.ID,
		)
		if res.Error != nil {
			return fmt.Errorf("update comment: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: comment.WorkspaceID,
			ActorID:     actorID,
			Action:      action,
			Target:      comment.ID.String(),
			Meta:        commentAuditMeta(comment, map[string]any{"resolved": comment.ResolvedAt != nil}),
		})
	})
}

func (r *commentRepository) DeleteComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`DELETE FROM comments WHERE id = ?`, comment.ID)
		if res.Error != nil {
			return fmt.Errorf("delete comment: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: comment.WorkspaceID,
			ActorID:     actorID,
			Action:      domainrepo.CommentActionDelete,
			Target:      comment.ID.String(),
			Meta:        commentAuditMeta(comment, nil),
		})
	})
}

func (r *commentRepository) EntityExists(ctx context.Context, workspaceID, entityID string) (bool, error) {
	var exists bool
	err := r.db.WithContext(ctx).Raw(
		`SELECT EXISTS (
		    SELECT 1 FROM entities
		     WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL
		 )`,
		workspaceID, entityID,
	).Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("check comment entity: %w", err)
	}
	return exists, nil
}

// commentAuditMeta builds the audit payload for a comment mutation. Extra
// fields are merged over the base entity/user pair.
func commentAuditMeta(comment *entity.Comment, extra map[string]any) json.RawMessage {
	meta := map[string]any{
		"entityId": comment.EntityID,
		"userId":   comment.UserID.String(),
	}
	for key, value := range extra {
		meta[key] = value
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		// Static string/string map: marshal cannot fail, but keep the audit
		// payload valid if it ever does.
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(encoded)
}

type commentRow struct {
	ID          uuid.UUID  `gorm:"column:id"`
	WorkspaceID string     `gorm:"column:workspace_id"`
	EntityID    string     `gorm:"column:entity_id"`
	UserID      uuid.UUID  `gorm:"column:user_id"`
	Body        string     `gorm:"column:body"`
	ResolvedAt  *time.Time `gorm:"column:resolved_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

func (row commentRow) toEntity() *entity.Comment {
	return &entity.Comment{
		ID:          row.ID,
		WorkspaceID: row.WorkspaceID,
		EntityID:    row.EntityID,
		UserID:      row.UserID,
		Body:        row.Body,
		ResolvedAt:  row.ResolvedAt,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}
