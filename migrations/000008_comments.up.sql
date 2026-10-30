-- 000008_comments: per-request comment threads.
--
-- A comment attaches to a synced entity (usually a `request`) by its opaque
-- workspace-local id, so there is intentionally no FK to entities(id): the
-- entity is validated by the API and may be removed independently.
-- `resolved_at` is NULL while the thread is open.

CREATE TABLE IF NOT EXISTS comments (
    id           UUID        NOT NULL,
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    entity_id    TEXT        NOT NULL,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body         TEXT        NOT NULL,
    resolved_at  TIMESTAMPTZ NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id)
);

-- Feed query: one entity's comments, newest first, keyset-paginated on
-- (created_at DESC, id DESC) with the last seen id as the cursor.
CREATE INDEX IF NOT EXISTS idx_comments_workspace_entity_created
    ON comments (workspace_id, entity_id, created_at DESC);
