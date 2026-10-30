-- 000004_entity_ops_retention: support time-based pruning of the idempotency
-- log.
--
-- entity_ops only has to answer "have I already applied this opId?" for
-- retries that arrive soon after the original push; it is not a cursor. A
-- retention job (cmd/prune) deletes rows older than a cutoff, which needs an
-- index on created_at to avoid a sequential scan.
--
-- entity_changes is deliberately NOT indexed for pruning: clients pull with a
-- seq cursor and may resume from a very old cursor, so those rows are retained.

CREATE INDEX idx_entity_ops_created_at ON entity_ops (created_at);
