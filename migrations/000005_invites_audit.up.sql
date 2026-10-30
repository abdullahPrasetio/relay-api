-- 000005_invites_audit: single-use invite acceptance and the audit log.
--
-- The invites table itself is owned by 000003; it already carries
-- accepted_at/accepted_by, which is how acceptance is made single-use. This
-- migration only adds the missing bookkeeping column/indexes and creates the
-- audit_events table (plan §3).

ALTER TABLE invites ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Invite lists are read per workspace, newest first.
CREATE INDEX IF NOT EXISTS idx_invites_workspace_created ON invites (workspace_id, created_at DESC);
-- Cleanup/expiry scans walk the deadline.
CREATE INDEX IF NOT EXISTS idx_invites_expires_at ON invites (expires_at);

CREATE TABLE IF NOT EXISTS audit_events (
    id           BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id TEXT        NOT NULL,
    actor_id     UUID        NOT NULL,
    action       TEXT        NOT NULL,
    target       TEXT        NOT NULL DEFAULT '',
    meta         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Audit pages are read per workspace newest-first with an id cursor.
CREATE INDEX IF NOT EXISTS idx_audit_events_workspace_id ON audit_events (workspace_id, id DESC);
