package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

type sessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) domainrepo.SessionRepository {
	return &sessionRepository{db: db}
}

func (r *sessionRepository) Create(ctx context.Context, session *entity.Session) error {
	if err := r.db.WithContext(ctx).Create(session).Error; err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *sessionRepository) FindByRefreshHash(ctx context.Context, refreshHash string) (*entity.Session, error) {
	return r.findByHash(ctx, "refresh_hash", refreshHash)
}

func (r *sessionRepository) FindByPrevHash(ctx context.Context, prevHash string) (*entity.Session, error) {
	return r.findByHash(ctx, "prev_hash", prevHash)
}

func (r *sessionRepository) findByHash(ctx context.Context, column, hash string) (*entity.Session, error) {
	var session entity.Session
	result := r.db.WithContext(ctx).Where(column+" = ?", hash).First(&session)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, gorm.ErrRecordNotFound
	}
	if result.Error != nil {
		return nil, fmt.Errorf("find session by %s: %w", column, result.Error)
	}
	return &session, nil
}

func (r *sessionRepository) FindByID(ctx context.Context, id uuid.UUID) (*entity.Session, error) {
	var session entity.Session
	result := r.db.WithContext(ctx).First(&session, "id = ?", id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, gorm.ErrRecordNotFound
	}
	if result.Error != nil {
		return nil, fmt.Errorf("find session by id: %w", result.Error)
	}
	return &session, nil
}

// Rotate is a single conditional UPDATE so two concurrent refreshes cannot
// both win: the loser sees 0 rows affected and falls back to the grace path.
func (r *sessionRepository) Rotate(
	ctx context.Context,
	id uuid.UUID,
	oldHash, newHash string,
	replacementCT []byte,
	prevExpiresAt, expiresAt, usedAt time.Time,
	deviceLabel string,
) (bool, error) {
	updates := map[string]any{
		"refresh_hash":    newHash,
		"prev_hash":       oldHash,
		"prev_expires_at": prevExpiresAt,
		"replacement_ct":  replacementCT,
		"expires_at":      expiresAt,
		"last_used_at":    usedAt,
	}
	if deviceLabel != "" {
		updates["device_label"] = deviceLabel
	}

	result := r.db.WithContext(ctx).
		Model(&entity.Session{}).
		Where("id = ? AND refresh_hash = ? AND revoked_at IS NULL", id, oldHash).
		Updates(updates)
	if result.Error != nil {
		return false, fmt.Errorf("rotate session: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *sessionRepository) RevokeByID(ctx context.Context, id uuid.UUID) error {
	err := r.db.WithContext(ctx).
		Model(&entity.Session{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", time.Now().UTC()).Error
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (r *sessionRepository) RevokeByUser(ctx context.Context, userID uuid.UUID) error {
	err := r.db.WithContext(ctx).
		Model(&entity.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now().UTC()).Error
	if err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

func (r *sessionRepository) ListByUser(ctx context.Context, userID uuid.UUID, now time.Time) ([]*entity.Session, error) {
	var sessions []*entity.Session
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, now).
		Order("created_at DESC").
		Find(&sessions).Error
	if err != nil {
		return nil, fmt.Errorf("list user sessions: %w", err)
	}
	return sessions, nil
}

func (r *sessionRepository) Touch(ctx context.Context, id uuid.UUID, at time.Time) error {
	err := r.db.WithContext(ctx).
		Model(&entity.Session{}).
		Where("id = ?", id).
		Update("last_used_at", at).Error
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}
