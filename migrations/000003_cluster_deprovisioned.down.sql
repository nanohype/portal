-- Postgres can't drop a single enum value, so reverse by recreating the type
-- without 'deprovisioned'. Any rows that reached the terminal teardown state are
-- remapped to 'committed' (the state they advanced from) so the column recast
-- can't fail on an unrepresentable value.
ALTER TABLE cluster_operations ALTER COLUMN status DROP DEFAULT;
UPDATE cluster_operations SET status = 'committed' WHERE status = 'deprovisioned';
ALTER TYPE cluster_op_status RENAME TO cluster_op_status_old;
CREATE TYPE cluster_op_status AS ENUM ('pending', 'committed', 'failed', 'active');
ALTER TABLE cluster_operations
    ALTER COLUMN status TYPE cluster_op_status USING status::text::cluster_op_status;
ALTER TABLE cluster_operations ALTER COLUMN status SET DEFAULT 'pending';
DROP TYPE cluster_op_status_old;
