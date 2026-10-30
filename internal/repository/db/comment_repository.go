package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// commentRepository stores per-entity comments and their mention sets.
// Mutations append their audit event in the same transaction, so an audit
// failure rolls the change back.
type commentRepository struct {
	db *gorm.DB
}

// NewCommentRepository returns the Postgres-backed comment repository.
func NewCommentRepository(db *gorm.DB) domainrepo.CommentRepository {
	return &commentRepository{db: db}
}

func (r *commentRepository) CreateComment(ctx context.Context, comment *entity.Comment, mentionIDs []uuid.UUID) error {
	now := time.Now().UTC()
	comment.CreatedAt = now
	comment.UpdatedAt = now

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if err := insertCommentMentions(tx, comment.ID, comment.WorkspaceID, mentionIDs, now); err != nil {
			return err
		}
		if err := insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: comment.WorkspaceID,
			ActorID:     comment.UserID,
			Action:      domainrepo.CommentActionCreate,
			Target:      comment.ID.String(),
			Meta:        commentAuditMeta(comment, nil),
		}); err != nil {
			return err
		}
		return hydrateComments(ctx, tx, []*entity.Comment{comment})
	})
	return err
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
	comment := row.toEntity()
	if err := hydrateComments(ctx, r.db, []*entity.Comment{comment}); err != nil {
		return nil, err
	}
	return comment, nil
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
	return hydrateRows(ctx, r.db, rows)
}

func (r *commentRepository) ListCommentsMentioning(ctx context.Context, workspaceID string, userID uuid.UUID, before *uuid.UUID, limit int) ([]entity.Comment, error) {
	query := `SELECT c.id, c.workspace_id, c.entity_id, c.user_id, c.body,
	                 c.resolved_at, c.created_at, c.updated_at
	            FROM comments c
	            JOIN comment_mentions cm ON cm.comment_id = c.id AND cm.user_id = ?
	           WHERE c.workspace_id = ?`
	args := []any{userID, workspaceID}

	if before != nil {
		var anchor time.Time
		row := r.db.WithContext(ctx).Raw(
			`SELECT c.created_at
			   FROM comments c
			   JOIN comment_mentions cm ON cm.comment_id = c.id AND cm.user_id = ?
			  WHERE c.id = ? AND c.workspace_id = ?`,
			userID, *before, workspaceID,
		).Row()
		if err := row.Scan(&anchor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, domainrepo.ErrCommentCursorNotFound
			}
			return nil, fmt.Errorf("resolve mention cursor: %w", err)
		}
		query += ` AND (c.created_at, c.id) < (?::timestamptz, ?::uuid)`
		args = append(args, anchor, *before)
	}

	query += ` ORDER BY c.created_at DESC, c.id DESC LIMIT ?`
	args = append(args, limit)

	var rows []commentRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list mentioned comments: %w", err)
	}
	return hydrateRows(ctx, r.db, rows)
}

func (r *commentRepository) UpdateComment(ctx context.Context, comment *entity.Comment, actorID uuid.UUID, action string, mentionIDs *[]uuid.UUID) error {
	now := time.Now().UTC()
	comment.UpdatedAt = now

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if mentionIDs != nil {
			if err := tx.Exec(`DELETE FROM comment_mentions WHERE comment_id = ?`, comment.ID).Error; err != nil {
				return fmt.Errorf("clear comment mentions: %w", err)
			}
			if err := insertCommentMentions(tx, comment.ID, comment.WorkspaceID, *mentionIDs, now); err != nil {
				return err
			}
		}
		if err := insertAudit(tx, &entity.AuditEvent{
			WorkspaceID: comment.WorkspaceID,
			ActorID:     actorID,
			Action:      action,
			Target:      comment.ID.String(),
			Meta:        commentAuditMeta(comment, map[string]any{"resolved": comment.ResolvedAt != nil}),
		}); err != nil {
			return err
		}
		return hydrateComments(ctx, tx, []*entity.Comment{comment})
	})
	return err
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

func (r *commentRepository) WorkspaceMemberNames(ctx context.Context, workspaceID string, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []memberNameRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT m.user_id, u.name
		   FROM workspace_members m
		   JOIN users u ON u.id = m.user_id
		   JOIN workspaces w ON w.id = m.workspace_id AND w.deleted_at IS NULL
		  WHERE m.workspace_id = ? AND m.user_id IN ?`,
		workspaceID, userIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list mention members: %w", err)
	}
	for _, row := range rows {
		out[row.UserID] = row.Name
	}
	return out, nil
}

// insertCommentMentions bulk-inserts the mention set of one comment. The
// caller has already validated and deduplicated the ids.
func insertCommentMentions(tx *gorm.DB, commentID uuid.UUID, workspaceID string, userIDs []uuid.UUID, now time.Time) error {
	if len(userIDs) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(userIDs))
	args := make([]any, 0, len(userIDs)*4)
	for _, userID := range userIDs {
		placeholders = append(placeholders, "(?, ?, ?, ?)")
		args = append(args, commentID, userID, workspaceID, now)
	}
	err := tx.Exec(
		`INSERT INTO comment_mentions (comment_id, user_id, workspace_id, created_at)
		 VALUES `+strings.Join(placeholders, ", ")+`
		 ON CONFLICT (comment_id, user_id) DO NOTHING`,
		args...,
	).Error
	if err != nil {
		return fmt.Errorf("insert comment mentions: %w", err)
	}
	return nil
}

// hydrateComments fills AuthorName and Mentions (with display names) for the
// given comments using one query for authors and one for mentions.
func hydrateComments(ctx context.Context, q *gorm.DB, comments []*entity.Comment) error {
	if len(comments) == 0 {
		return nil
	}
	authorIDs := make([]uuid.UUID, 0, len(comments))
	commentIDs := make([]uuid.UUID, 0, len(comments))
	for _, comment := range comments {
		comment.Mentions = []entity.CommentMention{}
		authorIDs = append(authorIDs, comment.UserID)
		commentIDs = append(commentIDs, comment.ID)
	}

	var authors []userNameRow
	if err := q.WithContext(ctx).Raw(
		`SELECT id, name FROM users WHERE id IN ?`, authorIDs,
	).Scan(&authors).Error; err != nil {
		return fmt.Errorf("hydrate comment authors: %w", err)
	}
	authorNames := make(map[uuid.UUID]string, len(authors))
	for _, author := range authors {
		authorNames[author.ID] = author.Name
	}

	var mentions []commentMentionRow
	if err := q.WithContext(ctx).Raw(
		`SELECT cm.comment_id, cm.user_id, u.name
		   FROM comment_mentions cm
		   JOIN users u ON u.id = cm.user_id
		  WHERE cm.comment_id IN ?
		  ORDER BY cm.created_at ASC, cm.user_id ASC`,
		commentIDs,
	).Scan(&mentions).Error; err != nil {
		return fmt.Errorf("hydrate comment mentions: %w", err)
	}

	byComment := make(map[uuid.UUID][]entity.CommentMention, len(comments))
	for _, mention := range mentions {
		byComment[mention.CommentID] = append(byComment[mention.CommentID], entity.CommentMention{
			UserID: mention.UserID,
			Name:   mention.Name,
		})
	}
	for _, comment := range comments {
		comment.AuthorName = authorNames[comment.UserID]
		if mentioned, ok := byComment[comment.ID]; ok {
			comment.Mentions = mentioned
		}
	}
	return nil
}

// hydrateRows converts scanned rows to entities and hydrates them.
func hydrateRows(ctx context.Context, q *gorm.DB, rows []commentRow) ([]entity.Comment, error) {
	out := make([]*entity.Comment, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toEntity())
	}
	if err := hydrateComments(ctx, q, out); err != nil {
		return nil, err
	}
	comments := make([]entity.Comment, 0, len(out))
	for _, comment := range out {
		comments = append(comments, *comment)
	}
	return comments, nil
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

type userNameRow struct {
	ID   uuid.UUID `gorm:"column:id"`
	Name string    `gorm:"column:name"`
}

type memberNameRow struct {
	UserID uuid.UUID `gorm:"column:user_id"`
	Name   string    `gorm:"column:name"`
}

// commentMentionRow is one mention of a comment, joined to the user name.
type commentMentionRow struct {
	CommentID uuid.UUID `gorm:"column:comment_id"`
	UserID    uuid.UUID `gorm:"column:user_id"`
	Name      string    `gorm:"column:name"`
}
