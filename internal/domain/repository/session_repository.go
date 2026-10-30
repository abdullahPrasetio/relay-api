package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

// SessionRepository persists refresh-token sessions in Postgres.
type SessionRepository interface {
	Create(ctx context.Context, session *entity.Session) error

	// FindByRefreshHash returns the session owning the current refresh token.
	FindByRefreshHash(ctx context.Context, refreshHash string) (*entity.Session, error)

	// FindByPrevHash returns the session whose last rotated-away token matches.
	FindByPrevHash(ctx context.Context, prevHash string) (*entity.Session, error)

	FindByID(ctx context.Context, id uuid.UUID) (*entity.Session, error)

	// Rotate atomically replaces oldHash with newHash for one session.
	// It returns false (without error) when the compare-and-swap lost, e.g. a
	// concurrent refresh already rotated the token.
	Rotate(ctx context.Context, id uuid.UUID, oldHash, newHash string, replacementCT []byte,
		prevExpiresAt, expiresAt, usedAt time.Time, deviceLabel string) (bool, error)

	RevokeByID(ctx context.Context, id uuid.UUID) error
	RevokeByUser(ctx context.Context, userID uuid.UUID) error

	// ListByUser returns non-revoked, unexpired sessions newest first.
	ListByUser(ctx context.Context, userID uuid.UUID, now time.Time) ([]*entity.Session, error)

	Touch(ctx context.Context, id uuid.UUID, at time.Time) error
}
