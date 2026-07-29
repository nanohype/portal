-- Author identity, resolved when the operation is enqueued.
--
-- portal authenticates to the GitOps repos as one deploy key, so every commit it
-- writes carries the same author. Who asked for the change lived only in the
-- commit body, as a user id, in prose that nothing machine-reads.
--
-- The identity is resolved at enqueue and stored here rather than looked up when
-- the commit is composed. A lookup at commit time returns nothing precisely when
-- the user has since been renamed or removed from the org — which is the case
-- someone is reading the log for. Storing it makes the record answer correctly
-- after the account is gone.
--
-- Nullable, and the workers omit the trailer when either is absent: an operation
-- enqueued before this column existed has no identity to report, and inventing
-- one would be worse than saying nothing.

ALTER TABLE tenant_operations
    ADD COLUMN created_by_name  TEXT,
    ADD COLUMN created_by_email TEXT;

ALTER TABLE cluster_operations
    ADD COLUMN created_by_name  TEXT,
    ADD COLUMN created_by_email TEXT;
