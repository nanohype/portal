// Hand-written pgx queries (sqlc-style); not generated, edit directly.

package repository

import "context"

const accountColumns = `id, org_id, name, description, aws_account_id, assume_role_arn, external_id_encrypted, default_region,
	vend_role_arn, data_kms_key_arn, cluster_permissions_boundary_arn, operator_permissions_boundary_arn,
	created_by, created_at, updated_at`

func scanAccount(row interface{ Scan(...interface{}) error }) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.OrgID, &a.Name, &a.Description, &a.AWSAccountID, &a.AssumeRoleARN, &a.ExternalIDEncrypted, &a.DefaultRegion,
		&a.VendRoleARN, &a.DataKmsKeyARN, &a.ClusterPermissionsBoundaryARN, &a.OperatorPermissionsBoundaryARN,
		&a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

type GetAccountParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) GetAccount(ctx context.Context, arg GetAccountParams) (Account, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return scanAccount(row)
}

type GetAccountByAWSIDParams struct {
	OrgID        string `json:"org_id"`
	AWSAccountID string `json:"aws_account_id"`
}

// GetAccountByAWSID resolves a portal account row from a 12-digit AWS account
// number. The provision watch-back uses it to map a vended Cluster's
// spec.account back to the portal account that carries the assume-role used to
// mint the new cluster's EKS tokens.
func (q *Queries) GetAccountByAWSID(ctx context.Context, arg GetAccountByAWSIDParams) (Account, error) {
	row := q.db.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE org_id = $1 AND aws_account_id = $2`,
		arg.OrgID, arg.AWSAccountID,
	)
	return scanAccount(row)
}

type ListAccountsParams struct {
	OrgID  string `json:"org_id"`
	Limit  int32  `json:"limit"`
	Offset int32  `json:"offset"`
}

func (q *Queries) ListAccounts(ctx context.Context, arg ListAccountsParams) ([]Account, error) {
	rows, err := q.db.Query(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		arg.OrgID, arg.Limit, arg.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	if accounts == nil {
		accounts = []Account{}
	}
	return accounts, rows.Err()
}

func (q *Queries) CountAccounts(ctx context.Context, orgID string) (int64, error) {
	row := q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM accounts WHERE org_id = $1`, orgID,
	)
	var count int64
	err := row.Scan(&count)
	return count, err
}

type CreateAccountParams struct {
	ID                  string `json:"id"`
	OrgID               string `json:"org_id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	AWSAccountID        string `json:"aws_account_id"`
	AssumeRoleARN       string `json:"assume_role_arn"`
	ExternalIDEncrypted string `json:"external_id_encrypted"`
	DefaultRegion       string `json:"default_region"`
	// Substrate prerequisites. Nil leaves the column NULL — an account that
	// vends same-account needs none of them.
	VendRoleARN                    *string `json:"vend_role_arn"`
	DataKmsKeyARN                  *string `json:"data_kms_key_arn"`
	ClusterPermissionsBoundaryARN  *string `json:"cluster_permissions_boundary_arn"`
	OperatorPermissionsBoundaryARN *string `json:"operator_permissions_boundary_arn"`
	CreatedBy                      string  `json:"created_by"`
}

func (q *Queries) CreateAccount(ctx context.Context, arg CreateAccountParams) (Account, error) {
	row := q.db.QueryRow(ctx,
		`INSERT INTO accounts (id, org_id, name, description, aws_account_id, assume_role_arn, external_id_encrypted, default_region,
			vend_role_arn, data_kms_key_arn, cluster_permissions_boundary_arn, operator_permissions_boundary_arn, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+accountColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.AWSAccountID, arg.AssumeRoleARN, arg.ExternalIDEncrypted, arg.DefaultRegion,
		arg.VendRoleARN, arg.DataKmsKeyARN, arg.ClusterPermissionsBoundaryARN, arg.OperatorPermissionsBoundaryARN, arg.CreatedBy,
	)
	return scanAccount(row)
}

type UpdateAccountParams struct {
	ID                  string `json:"id"`
	OrgID               string `json:"org_id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	AssumeRoleARN       string `json:"assume_role_arn"`
	ExternalIDEncrypted string `json:"external_id_encrypted"`
	DefaultRegion       string `json:"default_region"`
	// Pointers rather than strings, because the other fields on this struct use
	// "empty means leave alone" and these have to be clearable: an account that
	// stops vending cross-account must be able to drop its role, and an empty
	// string is the value that says so. Nil leaves the column untouched.
	VendRoleARN                    *string `json:"vend_role_arn"`
	DataKmsKeyARN                  *string `json:"data_kms_key_arn"`
	ClusterPermissionsBoundaryARN  *string `json:"cluster_permissions_boundary_arn"`
	OperatorPermissionsBoundaryARN *string `json:"operator_permissions_boundary_arn"`
}

func (q *Queries) UpdateAccount(ctx context.Context, arg UpdateAccountParams) (Account, error) {
	row := q.db.QueryRow(ctx,
		`UPDATE accounts
		SET name = COALESCE(NULLIF($3, ''), name),
			description = COALESCE(NULLIF($4, ''), description),
			assume_role_arn = COALESCE(NULLIF($5, ''), assume_role_arn),
			external_id_encrypted = COALESCE(NULLIF($6, ''), external_id_encrypted),
			default_region = COALESCE(NULLIF($7, ''), default_region),
			vend_role_arn = COALESCE($8, vend_role_arn),
			data_kms_key_arn = COALESCE($9, data_kms_key_arn),
			cluster_permissions_boundary_arn = COALESCE($10, cluster_permissions_boundary_arn),
			operator_permissions_boundary_arn = COALESCE($11, operator_permissions_boundary_arn),
			updated_at = NOW()
		WHERE id = $1 AND org_id = $2
		RETURNING `+accountColumns,
		arg.ID, arg.OrgID, arg.Name, arg.Description, arg.AssumeRoleARN, arg.ExternalIDEncrypted, arg.DefaultRegion,
		arg.VendRoleARN, arg.DataKmsKeyARN, arg.ClusterPermissionsBoundaryARN, arg.OperatorPermissionsBoundaryARN,
	)
	return scanAccount(row)
}

type DeleteAccountParams struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (q *Queries) DeleteAccount(ctx context.Context, arg DeleteAccountParams) error {
	_, err := q.db.Exec(ctx,
		`DELETE FROM accounts WHERE id = $1 AND org_id = $2`,
		arg.ID, arg.OrgID,
	)
	return err
}
