-- 000005_invites_audit: revert the audit log and invite indexes.

DROP TABLE IF EXISTS audit_events;
DROP INDEX IF EXISTS idx_audit_events_workspace_id;
DROP INDEX IF EXISTS idx_invites_expires_at;
DROP INDEX IF EXISTS idx_invites_workspace_created;
ALTER TABLE invites DROP COLUMN IF EXISTS created_at;
