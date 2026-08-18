package service

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
)

// VariableService owns the three variable scopes — org, pipeline and workspace.
//
// It exists because the decisions that make a variable safe are not transport
// concerns: a sensitive value is encrypted before it is stored, redacted before
// it is returned, redacted again before it reaches the audit log, and released
// in plaintext only once its disclosure is recorded. Those rules were previously
// written out in each of the three handlers, which meant a change had to land
// three times and no non-HTTP caller could reuse any of it.
//
// The three scopes keep separate methods rather than one generic one: their rows
// carry different owning ids (workspace_id, pipeline_id, org_id only), and the
// scoping predicate is the thing a cross-tenant bug hides in. Collapsing them
// would put the security-relevant difference behind a type parameter.
type VariableService struct {
	queries   *repository.Queries
	encryptor *secrets.Encryptor
	audit     auditRecorder
}

// auditRecorder is the audit surface this service needs. Narrowing to it keeps
// the constructor taking *AuditService while making the disclosure-failure
// branch reachable from a test.
//
// The seam answers a different question from the audit package's own tests, and
// both are needed: TestAuditServiceLogDisclosure proves LogDisclosure works
// against a real database, and a stub here proves the service refuses to return
// a secret when it does not. A stub could not establish the first, and the real
// method cannot be made to fail without breaking the table other tests share.
type auditRecorder interface {
	Log(ctx context.Context, entry AuditEntry)
	LogDisclosure(ctx context.Context, entry AuditEntry) error
}

func NewVariableService(queries *repository.Queries, encryptor *secrets.Encryptor, audit auditRecorder) *VariableService {
	return &VariableService{queries: queries, encryptor: encryptor, audit: audit}
}

// VariableInput is a caller's requested variable, before any of the rules below
// have been applied to it.
type VariableInput struct {
	Key         string
	Value       string
	Sensitive   bool
	Category    string
	Description string
}

// ActorMeta is who asked and from where. It exists so the audit entry can be
// built inside the service rather than assembled by each caller.
type ActorMeta struct {
	UserID    string
	IPAddress string
	UserAgent string
}

const (
	maxVariableKeyLen   = 256
	maxVariableValueLen = 65536
	redactedValue       = "***"
)

// Normalize applies the shape rules and fills the category default. It returns a
// validation error rather than an HTTP status, so the same rules hold for a
// caller that is not a request.
func (in VariableInput) Normalize() (VariableInput, error) {
	if in.Key == "" {
		return in, apperr.Validation("key is required")
	}
	if len(in.Key) > maxVariableKeyLen {
		return in, apperr.Validation(fmt.Sprintf("key must be at most %d characters", maxVariableKeyLen))
	}
	if len(in.Value) > maxVariableValueLen {
		return in, apperr.Validation("value must be at most 64KB")
	}
	if in.Category == "" {
		in.Category = "terraform"
	}
	if in.Category != "terraform" && in.Category != "env" {
		return in, apperr.Validation("category must be 'terraform' or 'env'")
	}
	return in, nil
}

// seal encrypts a sensitive value for storage. A nil encryptor means encryption
// is not configured, which is a development posture — the value is stored as
// given, and the caller still marks it sensitive so it is redacted on the way
// out.
func (s *VariableService) seal(in VariableInput) (string, error) {
	if !in.Sensitive || s.encryptor == nil {
		return in.Value, nil
	}
	sealed, err := s.encryptor.Encrypt(in.Value)
	if err != nil {
		return "", fmt.Errorf("encrypt variable value: %w", err)
	}
	return sealed, nil
}

// open decrypts a stored value for release.
func (s *VariableService) open(value string, sensitive bool) (string, error) {
	if !sensitive || s.encryptor == nil {
		return value, nil
	}
	plain, err := s.encryptor.Decrypt(value)
	if err != nil {
		return "", fmt.Errorf("decrypt variable value: %w", err)
	}
	return plain, nil
}

func newVariableID() string { return ulid.Make().String() }

// recordDisclosure writes the audit row for releasing a secret and returns the
// error, so a caller can refuse to release it. It is LogDisclosure rather than
// Log deliberately: AuditService.Log is best-effort by contract and explicitly
// not for credential operations, and a reveal whose audit row did not land is a
// secret that left the process unrecorded.
func (s *VariableService) recordDisclosure(ctx context.Context, orgID, action, entityType, entityID string, actor ActorMeta) error {
	return s.audit.LogDisclosure(ctx, AuditEntry{
		OrgID: orgID, UserID: actor.UserID,
		Action: action, EntityType: entityType, EntityID: entityID,
		IPAddress: actor.IPAddress, UserAgent: actor.UserAgent,
	})
}

func (s *VariableService) logChange(ctx context.Context, orgID, action, entityType, entityID string, before, after any, actor ActorMeta) {
	s.audit.Log(ctx, AuditEntry{
		OrgID: orgID, UserID: actor.UserID,
		Action: action, EntityType: entityType, EntityID: entityID,
		Before: before, After: after,
		IPAddress: actor.IPAddress, UserAgent: actor.UserAgent,
	})
}
