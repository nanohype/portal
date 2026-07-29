ALTER TABLE cluster_operations
    DROP COLUMN created_by_email,
    DROP COLUMN created_by_name;

ALTER TABLE tenant_operations
    DROP COLUMN created_by_email,
    DROP COLUMN created_by_name;
