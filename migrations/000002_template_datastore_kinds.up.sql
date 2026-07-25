-- A Platform declares its stateful substrate in spec.datastores, and the tenant
-- chart now emits it — so a template that allowlists the `datastores` override
-- path lets an operator declare one. The override allowlist alone is not a cap:
-- the path carries a whole list, so allowing it allows any kind, including a
-- relational store (an Aurora cluster) on a template meant for scratch tenants.
--
-- allowed_datastore_kinds is the same shape of cap as allowed_model_families —
-- when non-empty, an operator may narrow but not step outside it. Empty (the
-- default, and every existing row) means the template places no restriction,
-- which preserves current behavior exactly.
ALTER TABLE templates
    ADD COLUMN allowed_datastore_kinds JSONB NOT NULL DEFAULT '[]'::jsonb;
