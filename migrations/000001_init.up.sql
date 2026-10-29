-- 000001_init: base schema.
-- Kept portable across PostgreSQL 14+ and MySQL 8+ so either driver can be
-- migrated with the same file. Relay-specific tables get their own
-- Postgres-only migrations later.
--
-- Mirrors internal/domain/entity/user.go (GORM model) exactly, including the
-- index names GORM would create, so AutoMigrate in development is a no-op.

CREATE TABLE users (
    id         VARCHAR(36)  NOT NULL,
    name       VARCHAR(100) NOT NULL,
    email      VARCHAR(255) NOT NULL,
    password   VARCHAR(255) NOT NULL,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deleted_at TIMESTAMP(3) NULL,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX idx_users_email ON users (email);
CREATE INDEX idx_users_deleted_at ON users (deleted_at);
