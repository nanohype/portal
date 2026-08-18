package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
)

// stubAuditLogger fails the disclosure write on demand — the branch that decides
// whether a secret is released without a record of the release.
type stubAuditLogger struct {
	disclosureErr error
	disclosures   []service.AuditEntry
}

func (s *stubAuditLogger) Log(_ context.Context, _ service.AuditEntry) {}

func (s *stubAuditLogger) LogDisclosure(_ context.Context, entry service.AuditEntry) error {
	s.disclosures = append(s.disclosures, entry)
	return s.disclosureErr
}

// *service.AuditService must keep satisfying the seam, or a future edit widens
// the field back and the failure branch stops being reachable.
var _ auditLogger = (*service.AuditService)(nil)

// revealOrgVariable drives the real OrgVariableHandler.RevealValue against a
// real database, so the assertion is about the handler rather than a lookalike.
func revealOrgVariable(t *testing.T, audit auditLogger, plaintext string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "reveal")

	enc, err := secrets.NewEncryptor("test-encryption-key-32-bytes!!!!")
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	sealed, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	varID := newID()
	execSQL(t, ctx, `INSERT INTO org_variables (id,org_id,key,value,sensitive,category)
		VALUES ($1,$2,'aws_secret_access_key',$3,true,'env')`, varID, orgID, sealed)

	// Real service over the real database; only the audit recorder is faulted, so
	// this exercises handler → service → repository and isolates the one branch
	// under test.
	h := NewOrgVariableHandler(service.NewVariableService(testQueries, enc, audit))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/variables/"+varID+"/value", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("variableID", varID)
	reqCtx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	reqCtx = context.WithValue(reqCtx, auth.UserContextKey, &auth.UserContext{
		UserID: userID, OrgID: orgID, Role: "admin",
	})

	rec := httptest.NewRecorder()
	h.RevealValue(rec, req.WithContext(reqCtx))
	return rec
}

// AuditService.Log is best-effort by contract and explicitly not for credential
// operations: a failed write is logged and the caller proceeds. On a reveal that
// meant handing back a decrypted secret with no record that it left the process.
func TestRevealValue_WithholdsTheSecretWhenTheAuditWriteFails(t *testing.T) {
	requireDB(t)
	audit := &stubAuditLogger{disclosureErr: errors.New("audit table unavailable")}

	rec := revealOrgVariable(t, audit, "super-secret-value")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when the disclosure could not be recorded", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "super-secret-value") {
		t.Fatal("the plaintext was returned even though its audit row did not land")
	}
	if len(audit.disclosures) != 1 {
		t.Errorf("expected exactly one disclosure attempt, got %d", len(audit.disclosures))
	}
}

func TestRevealValue_ReturnsTheSecretOnceTheDisclosureIsRecorded(t *testing.T) {
	requireDB(t)
	audit := &stubAuditLogger{}

	rec := revealOrgVariable(t, audit, "super-secret-value")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["value"] != "super-secret-value" {
		t.Errorf("value = %q, want the decrypted plaintext", body["value"])
	}
	if len(audit.disclosures) != 1 || audit.disclosures[0].Action != "org_variable.reveal" {
		t.Errorf("expected one org_variable.reveal disclosure, got %+v", audit.disclosures)
	}
}
