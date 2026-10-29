-- 000001_init: base schema.
-- Relay is Postgres-only, so timestamps use TIMESTAMPTZ to preserve the
-- instant regardless of the server/client timezone.
--
-- Mirrors internal/domain/entity/user.go (GORM model) exactly, including the
-- index names GORM would create, so AutoMigrate in development is a no-op.

CREATE TABLE users (
    id         VARCHAR(36)  NOT NULL,
    name       VARCHAR(100) NOT NULL,
    email      VARCHAR(255) NOT NULL,
    password   VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ   NULL,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX idx_users_email ON users (email);
CREATE INDEX idx_users_deleted_at ON users (deleted_at);
