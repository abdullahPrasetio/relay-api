-- 000004_entity_ops_retention: revert the entity_ops retention index.

DROP INDEX IF EXISTS idx_entity_ops_created_at;
