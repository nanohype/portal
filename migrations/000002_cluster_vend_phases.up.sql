-- Vend phase timeline: a regressible map keyed by phase, e.g.
--   { "committed":     {"at": "...", "detail": ""},
--     "tofu_running":  {"at": "...", "detail": "applying ..."},
--     "active":        {"at": "...", "detail": ""} }
-- Portal projects the substrate's vend journey here. The substrate is the source
-- of truth, so a phase can move backward — a jsonb merge (`||`) overwrites a key,
-- which is exactly the regressible-projection behaviour we want. Portal-side
-- phases (committed/failed) are written by the order service; substrate phases
-- (tofu_running/active) are written later by the in-cluster watcher.
ALTER TABLE cluster_operations
    ADD COLUMN vend_phases JSONB NOT NULL DEFAULT '{}'::jsonb;
