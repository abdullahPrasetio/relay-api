-- 000003_workspaces: revert workspace/sync schema.

DROP TABLE IF EXISTS presence;
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS entity_ops;
DROP TABLE IF EXISTS entity_changes;
DROP TABLE IF EXISTS entities;
DROP TABLE IF EXISTS workspace_members;
DROP TABLE IF EXISTS workspaces;
