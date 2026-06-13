package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
)

type AccountService struct {
	queries *repository.Queries
	db      *pgxpool.Pool
	enc     *secrets.Encryptor
}

func NewAccountService(queries *repository.Queries, db *pgxpool.Pool, enc *secrets.Encryptor) *AccountService {
	return &AccountService{queries: queries, db: db, enc: enc}
}

type CreateAccountParams struct {
	OrgID         string
	Name          string
	Description   string
	AWSAccountID  string
	AssumeRoleARN string
	ExternalID    string
	DefaultRegion string
	CreatedBy     string
}

type UpdateAccountParams struct {
	ID            string
	OrgID         string
	Name          string
	Description   string
	AssumeRoleARN string
	ExternalID    string
	DefaultRegion string
}

func (s *AccountService) List(ctx context.Context, orgID string, page, perPage int) ([]repository.Account, int64, error) {
	offset := int32((page - 1) * perPage)

	accounts, err := s.queries.ListAccounts(ctx, repository.ListAccountsParams{
		OrgID:  orgID,
		Limit:  int32(perPage),
		Offset: offset,
	})
	if err != nil {
		return nil, 0, err
	}

	count, err := s.queries.CountAccounts(ctx, orgID)
	if err != nil {
		return nil, 0, err
	}

	return accounts, count, nil
}

func (s *AccountService) Get(ctx context.Context, id, orgID string) (repository.Account, error) {
	return s.queries.GetAccount(ctx, repository.GetAccountParams{ID: id, OrgID: orgID})
}

func (s *AccountService) Create(ctx context.Context, params CreateAccountParams) (repository.Account, error) {
	encExtID, err := s.encryptExternalID(params.ExternalID)
	if err != nil {
		return repository.Account{}, err
	}

	return s.queries.CreateAccount(ctx, repository.CreateAccountParams{
		ID:                  ulid.Make().String(),
		OrgID:               params.OrgID,
		Name:                params.Name,
		Description:         params.Description,
		AWSAccountID:        params.AWSAccountID,
		AssumeRoleARN:       params.AssumeRoleARN,
		ExternalIDEncrypted: encExtID,
		DefaultRegion:       params.DefaultRegion,
		CreatedBy:           params.CreatedBy,
	})
}

func (s *AccountService) Update(ctx context.Context, params UpdateAccountParams) (repository.Account, error) {
	encExtID, err := s.encryptExternalID(params.ExternalID)
	if err != nil {
		return repository.Account{}, err
	}

	return s.queries.UpdateAccount(ctx, repository.UpdateAccountParams{
		ID:                  params.ID,
		OrgID:               params.OrgID,
		Name:                params.Name,
		Description:         params.Description,
		AssumeRoleARN:       params.AssumeRoleARN,
		ExternalIDEncrypted: encExtID,
		DefaultRegion:       params.DefaultRegion,
	})
}

func (s *AccountService) Delete(ctx context.Context, id, orgID string) error {
	return s.queries.DeleteAccount(ctx, repository.DeleteAccountParams{ID: id, OrgID: orgID})
}

// DecryptExternalID returns the plaintext sts:ExternalId for an account, or ""
// when none is configured. AWS callers thread this into AssumeRoleConfig so a
// per-account role whose trust policy requires an external id (the house
// cross-account pattern, e.g. fleet-vend) can be assumed.
func (s *AccountService) DecryptExternalID(a repository.Account) (string, error) {
	if a.ExternalIDEncrypted == "" {
		return "", nil
	}
	plain, err := s.enc.Decrypt(a.ExternalIDEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt external_id: %w", err)
	}
	return plain, nil
}

// encryptExternalID returns ciphertext for a non-empty plaintext, or "" when
// plaintext is empty. The COALESCE/NULLIF pattern in UpdateAccount treats "" as
// "keep existing value", which preserves the behavior every other entity uses.
func (s *AccountService) encryptExternalID(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	enc, err := s.enc.Encrypt(plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypt external_id: %w", err)
	}
	return enc, nil
}
