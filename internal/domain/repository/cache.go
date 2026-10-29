package repository

import (
	"context"
	"time"
)

// Cacher defines cache operations that usecases depend on.
// Implementation lives in internal/repository/redis/.
type Cacher interface {
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Get(ctx context.Context, key string, dest interface{}) error
	Del(ctx context.Context, keys ...string) error
	Exists(ctx context.Context, key string) (bool, error)
}
