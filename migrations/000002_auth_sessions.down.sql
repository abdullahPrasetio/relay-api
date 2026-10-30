-- 000002_auth_sessions: revert sessions and restore the 000001 user id type.

DROP TABLE IF EXISTS sessions;

ALTER TABLE users ALTER COLUMN id TYPE VARCHAR(36) USING id::text;
