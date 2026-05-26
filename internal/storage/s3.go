package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/stxkxs/tofui/internal/domain"
)

type S3Storage struct {
	client *minio.Client
	bucket string
}

func NewS3Storage(cfg *domain.Config) (*S3Storage, error) {
	client, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: cfg.S3UseSSL,
		Region: cfg.S3Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}

	return &S3Storage{client: client, bucket: cfg.S3Bucket}, nil
}

func (s *S3Storage) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: "us-east-1"})
	}
	return nil
}

func (s *S3Storage) PutState(ctx context.Context, workspaceID string, serial int, data []byte) (string, error) {
	key := fmt.Sprintf("state/%s/%d.tfstate", workspaceID, serial)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/json",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload state: %w", err)
	}
	return key, nil
}

func (s *S3Storage) GetState(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (s *S3Storage) PutLog(ctx context.Context, runID string, phase string, data []byte) (string, error) {
	key := fmt.Sprintf("logs/%s/%s.log", runID, phase)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "text/plain",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload log: %w", err)
	}
	return key, nil
}

func (s *S3Storage) GetLog(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (s *S3Storage) PutPlanJSON(ctx context.Context, runID string, data []byte) (string, error) {
	key := fmt.Sprintf("plans/%s/plan.json", runID)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/json",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload plan JSON: %w", err)
	}
	return key, nil
}

func (s *S3Storage) GetPlanJSON(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (s *S3Storage) PutRawState(ctx context.Context, workspaceID string, serial int, data []byte) (string, error) {
	key := fmt.Sprintf("state-raw/%s/%d.tfstate", workspaceID, serial)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload raw state: %w", err)
	}
	return key, nil
}

func (s *S3Storage) GetRawState(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

// DeleteStateObjects removes both the browse-state and raw-state objects
// for a given workspace/serial. Missing objects are treated as success so
// the caller can recover from partial uploads without surfacing errors.
func (s *S3Storage) DeleteStateObjects(ctx context.Context, workspaceID string, serial int) error {
	for _, key := range []string{
		fmt.Sprintf("state/%s/%d.tfstate", workspaceID, serial),
		fmt.Sprintf("state-raw/%s/%d.tfstate", workspaceID, serial),
	} {
		if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
			// minio returns no-op success for missing keys, so any error here is real.
			return fmt.Errorf("failed to remove %s: %w", key, err)
		}
	}
	return nil
}

func (s *S3Storage) PutConfigArchive(ctx context.Context, workspaceID, configVersionID string, data []byte) (string, error) {
	key := fmt.Sprintf("configs/%s/%s.tar.gz", workspaceID, configVersionID)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload config archive: %w", err)
	}
	return key, nil
}

func (s *S3Storage) GetConfigArchive(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (s *S3Storage) PutModule(ctx context.Context, namespace, name, provider, version string, data []byte) (string, error) {
	key := fmt.Sprintf("modules/%s/%s/%s/%s.tar.gz", namespace, name, provider, version)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload module: %w", err)
	}
	return key, nil
}
