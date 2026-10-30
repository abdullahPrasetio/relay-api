-- 000002_auth_sessions: opaque refresh-token sessions (plan D5 / §3).
--
-- 000001 shipped users.id as VARCHAR(36) for scaffold portability, but the
-- target schema (plan §3) and the GORM User entity both use UUID. Align the
-- column here, before creating a uuid FK from sessions, so every later
-- migration can reference users(id) with uuid type checking.
ALTER TABLE users ALTER COLUMN id TYPE UUID USING id::uuid;

CREATE TABLE sessions (
    id             UUID        NOT NULL,
    user_id        UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_label   TEXT        NOT NULL DEFAULT '',
    refresh_hash   TEXT        NOT NULL,
    prev_hash      TEXT        NULL,
    prev_expires_at TIMESTAMPTZ NULL,
    -- AES-GCM envelope of the replacement token pair produced by the last
    -- rotation. Never plaintext: key is derived from JWT_SECRET.
    replacement_ct BYTEA       NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at   TIMESTAMPTZ NULL,
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX idx_sessions_refresh_hash ON sessions (refresh_hash);
CREATE INDEX idx_sessions_user_id ON sessions (user_id);
-- Grace lookups read the rotated-away token by its hash.
CREATE INDEX idx_sessions_prev_hash ON sessions (prev_hash);
