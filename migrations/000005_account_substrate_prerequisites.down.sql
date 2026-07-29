ALTER TABLE accounts
    DROP COLUMN operator_permissions_boundary_arn,
    DROP COLUMN cluster_permissions_boundary_arn,
    DROP COLUMN data_kms_key_arn,
    DROP COLUMN vend_role_arn;
