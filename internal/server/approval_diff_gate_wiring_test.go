package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nanohype/portal/internal/config"
	"github.com/nanohype/portal/internal/storage"
)

// The approval diff gate is armed by whether this instance is configured to
// store run artifacts, and the server is the only place that decides it. An
// instance with no object storage configured has no run that can carry a plan
// diff, so gating there refuses every approval; an instance that is configured
// and has no store has runs that lost a diff they should have had.
//
// The two are told apart by the configuration, not by whether a store object was
// built — those agree in every state but the one the gate exists for.

func serverWithStorage(t *testing.T, endpoint string, opts ...Option) *Server {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://portal:portal@127.0.0.1:1/portal?connect_timeout=1")
	if err != nil {
		t.Fatalf("build unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cfg := &config.Config{
		ServerAddr:    ":0",
		WebURL:        "http://localhost:5173",
		Environment:   "test",
		JWTSecret:     testJWTSecret,
		JWTExpiration: time.Hour,
		S3Endpoint:    endpoint,
	}
	opts = append(opts, WithAuthzResolver(stubAuthz{}))
	return New(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
}

func TestServer_ArmsTheApprovalDiffGateWhenArtifactStorageIsConfigured(t *testing.T) {
	s := serverWithStorage(t, "minio:9000")
	if !s.ApprovalService().RequiresPlanDiff() {
		t.Error("an instance configured to store run artifacts approves runs with no plan diff; the signature would cover changes nothing can show the signer")
	}
}

func TestServer_ExemptsTheApprovalDiffGateWhenNoArtifactStorageIsConfigured(t *testing.T) {
	s := serverWithStorage(t, "")
	if s.ApprovalService().RequiresPlanDiff() {
		t.Error("an instance with no object storage configured gates approvals on a diff no run of it can carry, so every approval-gated workspace is unusable")
	}
}

// The case the gate exists for, and the only one that separates "configured"
// from "a store was built": a configured endpoint that yields no store. Reading
// the store instead of the configuration answers this one wrongly, and answers
// every other state the same way — which is why nothing else can catch it.
func TestServer_ArmsTheApprovalDiffGateWhenConfiguredStorageIsUnavailable(t *testing.T) {
	unavailable := func(*config.Config) (*storage.S3Storage, error) {
		return nil, errors.New("dial tcp: connection refused")
	}
	s := serverWithStorage(t, "minio:9000", WithArtifactStore(unavailable))

	if !s.ApprovalService().RequiresPlanDiff() {
		t.Error("an instance whose configured object storage is unavailable exempted its approvals; those runs lost a diff they should have had, and the signature covers changes nothing recorded")
	}
}

// An instance with no endpoint never builds a store, and must stay exempt even
// though the store is nil for the same reason the case above is armed.
func TestServer_StaysExemptWhenNoEndpointIsConfiguredAndNoStoreIsBuilt(t *testing.T) {
	built := false
	s := serverWithStorage(t, "", WithArtifactStore(func(*config.Config) (*storage.S3Storage, error) {
		built = true
		return nil, errors.New("should not be called")
	}))

	if built {
		t.Error("the server tried to build object storage for an instance that configured none")
	}
	if s.ApprovalService().RequiresPlanDiff() {
		t.Error("an instance with no endpoint gates approvals on a diff no run of it can carry")
	}
}
