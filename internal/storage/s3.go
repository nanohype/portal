package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nanohype/portal/internal/config"
)

// requestTimeout bounds every S3 round-trip, body transfer included (all
// objects here are read fully into memory, so the client timeout covers the
// whole operation). Callers pass a context too, but in the worker that
// context is the multi-hour job deadline — without a per-request bound a
// wedged connection would hold a run open for the entire job budget. Two
// minutes clears the largest state files and config archives with room to
// spare.
const requestTimeout = 2 * time.Minute

// S3Storage persists OpenTofu state, run logs, plan JSON, config archives, and
// module bundles in an S3-compatible object store, via the AWS SDK (the same SDK
// the rest of portal's AWS access uses). It speaks the generic S3 API, so it
// works against AWS S3 — the production hub, via IRSA — and any S3-compatible
// store (a dev minio / SeaweedFS, via static keys + a custom endpoint).
type S3Storage struct {
	client *s3.Client
	bucket string
}

func NewS3Storage(cfg *config.Config) (*S3Storage, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
	}
	if cfg.S3AccessKey != "" && cfg.S3SecretKey != "" {
		// Explicit static keys — dev, or any non-IRSA S3-compatible store.
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		))
	}
	// With no static keys the SDK's default chain supplies credentials — env,
	// then EKS IRSA web-identity / EC2 / ECS. That's the production hub path: the
	// worker's IRSA role grants S3 access and no long-lived key sits at rest.
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config for s3: %w", err)
	}

	// Config carries the endpoint without a scheme (e.g. localhost:9000,
	// s3.us-west-2.amazonaws.com); derive the scheme from S3UseSSL.
	endpoint := cfg.S3Endpoint
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		scheme := "https://"
		if !cfg.S3UseSSL {
			scheme = "http://"
		}
		endpoint = scheme + endpoint
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		// Path-style addressing works against self-hosted S3-compatible stores
		// and AWS S3 alike, so portal doesn't depend on per-bucket virtual-host DNS.
		o.UsePathStyle = true
	})

	return &S3Storage{client: client, bucket: cfg.S3Bucket}, nil
}

// EnsureBucket creates the bucket if it doesn't already exist. In production the
// bucket is provisioned out of band, so HeadBucket succeeds and nothing is
// created; the create path is the self-contained-dev convenience.
func (s *S3Storage) EnsureBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket}); err == nil {
		return nil
	}
	if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if errors.As(err, &owned) || errors.As(err, &exists) {
			return nil // already there (pre-existing, or a concurrent create)
		}
		return fmt.Errorf("ensure bucket %q: %w", s.bucket, err)
	}
	return nil
}

// put uploads bytes under key with the given content type, returning the key.
func (s *S3Storage) put(ctx context.Context, key, contentType string, data []byte) (string, error) {
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &key,
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   &contentType,
	}); err != nil {
		return "", fmt.Errorf("upload %s: %w", key, err)
	}
	return key, nil
}

// get reads the object at key fully into memory.
func (s *S3Storage) get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *S3Storage) PutState(ctx context.Context, workspaceID string, serial int, data []byte) (string, error) {
	return s.put(ctx, fmt.Sprintf("state/%s/%d.tfstate", workspaceID, serial), "application/json", data)
}

func (s *S3Storage) GetState(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key)
}

func (s *S3Storage) PutLog(ctx context.Context, runID, phase string, data []byte) (string, error) {
	return s.put(ctx, fmt.Sprintf("logs/%s/%s.log", runID, phase), "text/plain", data)
}

func (s *S3Storage) GetLog(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key)
}

func (s *S3Storage) PutPlanJSON(ctx context.Context, runID string, data []byte) (string, error) {
	return s.put(ctx, fmt.Sprintf("plans/%s/plan.json", runID), "application/json", data)
}

func (s *S3Storage) GetPlanJSON(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key)
}

func (s *S3Storage) PutRawState(ctx context.Context, workspaceID string, serial int, data []byte) (string, error) {
	return s.put(ctx, fmt.Sprintf("state-raw/%s/%d.tfstate", workspaceID, serial), "application/octet-stream", data)
}

func (s *S3Storage) GetRawState(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key)
}

// DeleteStateObjects removes both the browse-state and raw-state objects for a
// workspace/serial. S3 DeleteObject is idempotent (a missing key returns
// success), so partial uploads clean up without surfacing errors.
func (s *S3Storage) DeleteStateObjects(ctx context.Context, workspaceID string, serial int) error {
	for _, key := range []string{
		fmt.Sprintf("state/%s/%d.tfstate", workspaceID, serial),
		fmt.Sprintf("state-raw/%s/%d.tfstate", workspaceID, serial),
	} {
		k := key
		if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &k}); err != nil {
			return fmt.Errorf("remove %s: %w", key, err)
		}
	}
	return nil
}

func (s *S3Storage) PutConfigArchive(ctx context.Context, workspaceID, configVersionID string, data []byte) (string, error) {
	return s.put(ctx, fmt.Sprintf("configs/%s/%s.tar.gz", workspaceID, configVersionID), "application/gzip", data)
}

func (s *S3Storage) GetConfigArchive(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key)
}

func (s *S3Storage) PutModule(ctx context.Context, namespace, name, provider, version string, data []byte) (string, error) {
	return s.put(ctx, fmt.Sprintf("modules/%s/%s/%s/%s.tar.gz", namespace, name, provider, version), "application/gzip", data)
}
