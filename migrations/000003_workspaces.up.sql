-- 000003_workspaces: team workspaces and per-entity delta sync.
--
-- Workspace and entity identifiers are opaque text produced by the desktop
-- app (`ws_…`, `col_…`, `req_…`, `env_…`), not UUIDs. User identifiers remain
-- UUIDs referencing users(id).
--
-- Realtime, auth sessions, monitors and audit live in their own migrations.

CREATE TABLE workspaces (
    id         TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    owner_id   UUID        NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ NULL,
    PRIMARY KEY (id)
);

CREATE TABLE workspace_members (
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         TEXT        NOT NULL CHECK (role IN ('owner','admin','editor','viewer')),
    invited_by   UUID        NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX idx_workspace_members_user ON workspace_members (user_id);

CREATE TABLE entities (
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    id           TEXT        NOT NULL,
    kind         TEXT        NOT NULL CHECK (kind IN ('collection','folder','request','environment','globals')),
    parent_id    TEXT        NULL,
    order_key    TEXT        NULL,
    rev          BIGINT      NOT NULL DEFAULT 1,
    payload      JSONB       NOT NULL,
    deleted_at   TIMESTAMPTZ NULL,
    updated_by   UUID        NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, id)
);

CREATE INDEX idx_entities_workspace_parent ON entities (workspace_id, parent_id);

-- Append-only pull log. seq is global (identity) but read per workspace; the
-- client cursor is the last seq it has applied.
CREATE TABLE entity_changes (
    seq          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    entity_id    TEXT        NOT NULL,
    rev          BIGINT      NOT NULL,
    deleted      BOOLEAN     NOT NULL
);

CREATE INDEX idx_entity_changes_workspace_seq ON entity_changes (workspace_id, seq);

-- Idempotency: a retry with the same opId returns the stored result and is not
-- applied a second time.
CREATE TABLE entity_ops (
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    op_id        TEXT        NOT NULL,
    entity_id    TEXT        NOT NULL,
    rev          BIGINT      NOT NULL,
    result       JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, op_id)
);

CREATE TABLE invites (
    id          UUID        NOT NULL,
    workspace_id TEXT       NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    role        TEXT        NOT NULL CHECK (role IN ('admin','editor','viewer')),
    token_hash  TEXT        NOT NULL UNIQUE,
    created_by  UUID        NOT NULL REFERENCES users(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ NULL,
    accepted_by UUID        NULL REFERENCES users(id),
    PRIMARY KEY (id)
);

CREATE TABLE presence (
    workspace_id TEXT        NOT NULL,
    user_id      UUID        NOT NULL,
    entity_id    TEXT        NULL,
    last_seen    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);
