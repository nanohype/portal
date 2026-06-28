-- Postgres can't drop a single enum value, so reverse by recreating the type
-- without 'unwedge'. Any rows recorded under it are remapped to 'deprovision'
-- (the lifecycle intent unwedge served) so the column recast can't fail on an
-- unrepresentable value.
UPDATE cluster_operations SET operation = 'deprovision' WHERE operation = 'unwedge';
ALTER TYPE cluster_op_kind RENAME TO cluster_op_kind_old;
CREATE TYPE cluster_op_kind AS ENUM ('provision', 'deprovision');
ALTER TABLE cluster_operations
    ALTER COLUMN operation TYPE cluster_op_kind USING operation::text::cluster_op_kind;
DROP TYPE cluster_op_kind_old;
