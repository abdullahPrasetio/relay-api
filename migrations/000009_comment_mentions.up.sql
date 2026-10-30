-- 000009_comment_mentions: users mentioned in a comment body.
--
-- One row per (comment, user) pair. workspace_id is denormalised so the
-- "mentions for me" feed can be served by a single index without joining
-- comments, and so a workspace-wide purge is a plain cascade. Rows disappear
-- with either the comment or the user.

CREATE TABLE IF NOT EXISTS comment_mentions (
    comment_id   UUID        NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, user_id)
);

-- Mentions feed: one user's mentions in a workspace, newest first. The
-- comment id from comments is the keyset tiebreaker, so the feed joins
-- comments on the same (created_at DESC, id DESC) order.
CREATE INDEX IF NOT EXISTS idx_comment_mentions_user_workspace_created
    ON comment_mentions (user_id, workspace_id, created_at DESC);
