package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
	"github.com/nanohype/portal/internal/service"
)

func newVariableSvc(t *testing.T) *service.VariableService {
	t.Helper()
	enc, err := secrets.NewEncryptor("test-encryption-key-32-bytes!!!!")
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	return service.NewVariableService(testQueries, enc, service.NewAuditService(testQueries))
}

// Normalize carries the shape rules that used to be restated in each of the
// three handlers. They are pure, so they are tested without a database.
func TestVariableInputNormalize(t *testing.T) {
	t.Run("category defaults to terraform", func(t *testing.T) {
		got, err := service.VariableInput{Key: "k", Value: "v"}.Normalize()
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if got.Category != "terraform" {
			t.Errorf("category = %q, want terraform", got.Category)
		}
	})

	for _, tc := range []struct {
		name string
		in   service.VariableInput
		want string
	}{
		{"key is required", service.VariableInput{Value: "v"}, "key is required"},
		{"key length is capped", service.VariableInput{Key: strings.Repeat("k", 257), Value: "v"}, "at most 256"},
		{"value length is capped", service.VariableInput{Key: "k", Value: strings.Repeat("v", 65537)}, "64KB"},
		{"category is an enum", service.VariableInput{Key: "k", Value: "v", Category: "shell"}, "terraform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.in.Normalize()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindValidation {
				t.Fatalf("want a validation error (400 at the edge), got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// A sensitive value is encrypted at rest and redacted on the way out. Both halves
// matter: storing plaintext leaks to anyone reading the table, and returning it
// leaks to anyone reading the API.
func TestVariableServiceSealsAndRedacts(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, _ := seedOrg(t, ctx, "varseal")
	svc := newVariableSvc(t)

	v, err := svc.CreateOrgVariable(ctx, orgID, service.VariableInput{
		Key: "aws_secret_access_key", Value: "super-secret", Sensitive: true, Category: "env",
	}, service.ActorMeta{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if v.Value != "***" {
		t.Errorf("returned value = %q, want it redacted", v.Value)
	}

	// Stored form must be neither the plaintext nor the redaction marker.
	raw, err := testQueries.GetOrgVariable(ctx, repository.GetOrgVariableParams{ID: v.ID, OrgID: orgID})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if raw.Value == "super-secret" {
		t.Error("value was stored in plaintext")
	}
	if raw.Value == "***" {
		t.Error("the redaction marker was stored instead of the ciphertext")
	}

	// And it round-trips through reveal.
	plain, err := svc.RevealOrgVariable(ctx, orgID, v.ID, service.ActorMeta{})
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if plain != "super-secret" {
		t.Errorf("revealed %q, want the original plaintext", plain)
	}

	// A non-sensitive value is stored and returned as-is.
	plainVar, err := svc.CreateOrgVariable(ctx, orgID, service.VariableInput{
		Key: "AWS_REGION", Value: "us-east-1", Category: "env",
	}, service.ActorMeta{})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if plainVar.Value != "us-east-1" {
		t.Errorf("non-sensitive value = %q, want it untouched", plainVar.Value)
	}
}

// The precedence the worker applies at run time, which this view exists to
// predict: org < pipeline < workspace.
func TestVariableServiceEffectivePrecedence(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "vareffective")
	wsID := seedWorkspace(t, ctx, orgID, userID)
	svc := newVariableSvc(t)

	actor := service.ActorMeta{UserID: userID}
	if _, err := svc.CreateOrgVariable(ctx, orgID, service.VariableInput{Key: "TF_LOG", Value: "org", Category: "env"}, actor); err != nil {
		t.Fatalf("org var: %v", err)
	}
	if _, err := svc.CreateWorkspaceVariable(ctx, orgID, wsID, service.VariableInput{Key: "TF_LOG", Value: "workspace", Category: "env"}, actor); err != nil {
		t.Fatalf("ws var: %v", err)
	}
	if _, err := svc.CreateOrgVariable(ctx, orgID, service.VariableInput{Key: "ORG_ONLY", Value: "kept", Category: "env"}, actor); err != nil {
		t.Fatalf("org only: %v", err)
	}

	effective, err := svc.EffectiveVariables(ctx, orgID, wsID, "")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}

	byKey := map[string]service.EffectiveVariable{}
	for _, v := range effective {
		byKey[v.Key] = v
	}
	if got := byKey["TF_LOG"]; got.Value != "workspace" || got.Source != "workspace" {
		t.Errorf("TF_LOG = %q from %q, want workspace winning over org", got.Value, got.Source)
	}
	if got := byKey["ORG_ONLY"]; got.Value != "kept" || got.Source != "org" {
		t.Errorf("ORG_ONLY = %q from %q, want the org layer preserved", got.Value, got.Source)
	}
}

// A batch is rejected whole. Applying half of one leaves a workspace in a state
// the caller never asked for and cannot tell apart from success.
func TestVariableServiceBulkCreateRejectsWholeBatches(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	orgID, userID := seedOrg(t, ctx, "varbulk")
	wsID := seedWorkspace(t, ctx, orgID, userID)
	svc := newVariableSvc(t)

	t.Run("an empty batch is refused", func(t *testing.T) {
		if _, err := svc.BulkCreateWorkspaceVariables(ctx, orgID, wsID, nil, service.ActorMeta{}); err == nil {
			t.Fatal("expected an empty batch to be refused")
		}
	})

	t.Run("a duplicate key refuses the batch before writing any of it", func(t *testing.T) {
		_, err := svc.BulkCreateWorkspaceVariables(ctx, orgID, wsID, []service.VariableInput{
			{Key: "A", Value: "1", Category: "env"},
			{Key: "A", Value: "2", Category: "env"},
		}, service.ActorMeta{})
		if err == nil {
			t.Fatal("expected a duplicate key to be refused")
		}
		vars, err := svc.ListWorkspaceVariables(ctx, orgID, wsID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vars) != 0 {
			t.Errorf("batch was partially applied: %d rows written", len(vars))
		}
	})

	t.Run("a bad category in the last entry refuses the whole batch", func(t *testing.T) {
		_, err := svc.BulkCreateWorkspaceVariables(ctx, orgID, wsID, []service.VariableInput{
			{Key: "GOOD", Value: "1", Category: "env"},
			{Key: "BAD", Value: "2", Category: "shell"},
		}, service.ActorMeta{})
		if err == nil {
			t.Fatal("expected a bad category to be refused")
		}
		vars, err := svc.ListWorkspaceVariables(ctx, orgID, wsID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(vars) != 0 {
			t.Errorf("the valid entry was written before the batch failed: %d rows", len(vars))
		}
	})

	t.Run("a valid batch is written and redacted", func(t *testing.T) {
		created, err := svc.BulkCreateWorkspaceVariables(ctx, orgID, wsID, []service.VariableInput{
			{Key: "PLAIN", Value: "visible", Category: "env"},
			{Key: "SECRET", Value: "hidden", Sensitive: true, Category: "env"},
		}, service.ActorMeta{})
		if err != nil {
			t.Fatalf("bulk create: %v", err)
		}
		if len(created) != 2 {
			t.Fatalf("created %d, want 2", len(created))
		}
		for _, v := range created {
			if v.Sensitive && v.Value != "***" {
				t.Errorf("%s came back unredacted", v.Key)
			}
		}
	})
}
