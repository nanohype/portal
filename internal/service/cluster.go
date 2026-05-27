package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/riverqueue/river"

	"github.com/stxkxs/tofui/internal/repository"
	"github.com/stxkxs/tofui/internal/secrets"
	"github.com/stxkxs/tofui/internal/worker"
)

type ClusterService struct {
	queries     *repository.Queries
	db          *pgxpool.Pool
	enc         *secrets.Encryptor
	riverClient *river.Client[pgx.Tx]
}

func NewClusterService(queries *repository.Queries, db *pgxpool.Pool, enc *secrets.Encryptor) *ClusterService {
	return &ClusterService{queries: queries, db: db, enc: enc}
}

func (s *ClusterService) SetRiverClient(client *river.Client[pgx.Tx]) {
	s.riverClient = client
}

// EnqueueConnectionTest schedules the async connection-test job for a
// cluster. Called from the handler on create/update and from the manual
// "Test connection" endpoint. No-op (with warning) if River isn't wired —
// degraded-dev mode shouldn't crash on a missing connection-test.
func (s *ClusterService) EnqueueConnectionTest(ctx context.Context, clusterID, orgID string) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not configured")
	}
	_, err := s.riverClient.Insert(ctx, worker.ClusterConnectionTestJobArgs{
		ClusterID: clusterID,
		OrgID:     orgID,
	}, nil)
	return err
}

type CreateClusterParams struct {
	OrgID       string
	AccountID   string
	Name        string
	Description string
	Environment string
	APIEndpoint string
	CABundle    string // plaintext PEM; encrypted before persist
	SAToken     string // plaintext bearer token; encrypted before persist
	Region      string
	CreatedBy   string
}

type UpdateClusterParams struct {
	ID          string
	OrgID       string
	Name        string
	Description string
	Environment string
	APIEndpoint string
	CABundle    string // empty = unchanged (COALESCE/NULLIF pattern)
	SAToken     string // empty = unchanged
	Region      string
}

// ClusterCreds carries the decrypted credentials for a cluster. Built by
// Decrypt for use by the connection-test worker / future k8s callers.
type ClusterCreds struct {
	APIEndpoint string
	CABundle    []byte
	SAToken     string
}

func (s *ClusterService) List(ctx context.Context, orgID, accountID string, page, perPage int) ([]repository.Cluster, int64, error) {
	offset := int32((page - 1) * perPage)

	clusters, err := s.queries.ListClusters(ctx, repository.ListClustersParams{
		OrgID:     orgID,
		AccountID: accountID,
		Limit:     int32(perPage),
		Offset:    offset,
	})
	if err != nil {
		return nil, 0, err
	}

	count, err := s.queries.CountClusters(ctx, repository.CountClustersParams{
		OrgID: orgID, AccountID: accountID,
	})
	if err != nil {
		return nil, 0, err
	}

	return clusters, count, nil
}

func (s *ClusterService) Get(ctx context.Context, id, orgID string) (repository.Cluster, error) {
	return s.queries.GetCluster(ctx, repository.GetClusterParams{ID: id, OrgID: orgID})
}

func (s *ClusterService) Create(ctx context.Context, params CreateClusterParams) (repository.Cluster, error) {
	caEnc, err := s.enc.Encrypt(params.CABundle)
	if err != nil {
		return repository.Cluster{}, fmt.Errorf("encrypt ca bundle: %w", err)
	}
	tokenEnc, err := s.enc.Encrypt(params.SAToken)
	if err != nil {
		return repository.Cluster{}, fmt.Errorf("encrypt sa token: %w", err)
	}

	return s.queries.CreateCluster(ctx, repository.CreateClusterParams{
		ID:                ulid.Make().String(),
		OrgID:             params.OrgID,
		AccountID:         params.AccountID,
		Name:              params.Name,
		Description:       params.Description,
		Environment:       params.Environment,
		APIEndpoint:       params.APIEndpoint,
		CABundleEncrypted: caEnc,
		SATokenEncrypted:  tokenEnc,
		Region:            params.Region,
		CreatedBy:         params.CreatedBy,
	})
}

func (s *ClusterService) Update(ctx context.Context, params UpdateClusterParams) (repository.Cluster, error) {
	caEnc, err := s.encryptIfSet(params.CABundle)
	if err != nil {
		return repository.Cluster{}, fmt.Errorf("encrypt ca bundle: %w", err)
	}
	tokenEnc, err := s.encryptIfSet(params.SAToken)
	if err != nil {
		return repository.Cluster{}, fmt.Errorf("encrypt sa token: %w", err)
	}

	return s.queries.UpdateCluster(ctx, repository.UpdateClusterParams{
		ID:                params.ID,
		OrgID:             params.OrgID,
		Name:              params.Name,
		Description:       params.Description,
		Environment:       params.Environment,
		APIEndpoint:       params.APIEndpoint,
		CABundleEncrypted: caEnc,
		SATokenEncrypted:  tokenEnc,
		Region:            params.Region,
	})
}

func (s *ClusterService) Delete(ctx context.Context, id, orgID string) error {
	return s.queries.DeleteCluster(ctx, repository.DeleteClusterParams{ID: id, OrgID: orgID})
}

// Decrypt returns the plaintext credentials needed to talk to the cluster.
// Used by the connection-test job and by future read-side workers.
func (s *ClusterService) Decrypt(c repository.Cluster) (ClusterCreds, error) {
	ca, err := s.enc.Decrypt(c.CABundleEncrypted)
	if err != nil {
		return ClusterCreds{}, fmt.Errorf("decrypt ca bundle: %w", err)
	}
	token, err := s.enc.Decrypt(c.SATokenEncrypted)
	if err != nil {
		return ClusterCreds{}, fmt.Errorf("decrypt sa token: %w", err)
	}
	return ClusterCreds{
		APIEndpoint: c.APIEndpoint,
		CABundle:    []byte(ca),
		SAToken:     token,
	}, nil
}

// SetConnectionStatus is the write path the connection-test worker uses to
// report its results back. Pulled out so callers don't need to know the
// success-vs-failure column shape.
func (s *ClusterService) SetConnectionStatus(ctx context.Context, id, orgID, status, errMsg, k8sVersion string, nodeCount int32) error {
	return s.queries.SetClusterConnectionStatus(ctx, repository.SetClusterConnectionStatusParams{
		ID:         id,
		OrgID:      orgID,
		Status:     status,
		Error:      errMsg,
		NodeCount:  nodeCount,
		K8sVersion: k8sVersion,
		At:         time.Now(),
	})
}

func (s *ClusterService) encryptIfSet(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return s.enc.Encrypt(plaintext)
}
