-- Per-account substrate prerequisites, stored on the account rather than the order.
--
-- clusterspec.Input already expresses four references landing-zone publishes to
-- SSM and the vend does not create: the fleet-vend role a cross-account vend
-- assumes, the data KMS key, and the two permissions boundaries that cap the IAM
-- such a vend mints. They are properties of the account, not of any one order —
-- the same account answers the same way for every cluster vended into it.
--
-- Without them here, a cross-account vend is only reachable by a caller who knows
-- three ARNs and pastes them per order, because Input.Validate refuses a
-- vend_role_arn that does not also carry both boundaries. portal already resolves
-- the ordering account row at order time to stamp its spoke role, so stamping
-- these costs a lookup that already happens.
--
-- All nullable: an account vending same-account needs none of them, and empty
-- means ungated, which is the Cluster XRD's default.

ALTER TABLE accounts
    ADD COLUMN vend_role_arn                     TEXT,
    ADD COLUMN data_kms_key_arn                  TEXT,
    ADD COLUMN cluster_permissions_boundary_arn  TEXT,
    ADD COLUMN operator_permissions_boundary_arn TEXT;
