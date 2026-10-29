package service

import (
	"context"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

type ExternalUserService interface {
	GetUser(ctx context.Context, id string) (*entity.User, error)
}
