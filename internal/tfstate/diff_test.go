package tfstate

import (
	"testing"
)

func TestDiffStates_AddedResource(t *testing.T) {
	from := `{"version": 4, "resources": []}`
	to := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-123"}}]}
	]}`

	diff, err := DiffStates([]byte(from), []byte(to), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	if diff.Added != 1 {
		t.Errorf("Added = %d, want 1", diff.Added)
	}
	if diff.Removed != 0 {
		t.Errorf("Removed = %d, want 0", diff.Removed)
	}
	if diff.Changed != 0 {
		t.Errorf("Changed = %d, want 0", diff.Changed)
	}
	if len(diff.Diffs) != 1 {
		t.Fatalf("len(Diffs) = %d, want 1", len(diff.Diffs))
	}
	if diff.Diffs[0].Action != "added" {
		t.Errorf("Diffs[0].Action = %q, want %q", diff.Diffs[0].Action, "added")
	}
}

func TestDiffStates_RemovedResource(t *testing.T) {
	from := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-123"}}]}
	]}`
	to := `{"version": 4, "resources": []}`

	diff, err := DiffStates([]byte(from), []byte(to), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	if diff.Removed != 1 {
		t.Errorf("Removed = %d, want 1", diff.Removed)
	}
	if len(diff.Diffs) != 1 {
		t.Fatalf("len(Diffs) = %d, want 1", len(diff.Diffs))
	}
	if diff.Diffs[0].Action != "removed" {
		t.Errorf("Diffs[0].Action = %q, want %q", diff.Diffs[0].Action, "removed")
	}
}

func TestDiffStates_ChangedResource(t *testing.T) {
	from := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-123", "instance_type": "t3.micro"}}]}
	]}`
	to := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-123", "instance_type": "t3.large"}}]}
	]}`

	diff, err := DiffStates([]byte(from), []byte(to), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	if diff.Changed != 1 {
		t.Errorf("Changed = %d, want 1", diff.Changed)
	}
	if len(diff.Diffs) != 1 {
		t.Fatalf("len(Diffs) = %d, want 1", len(diff.Diffs))
	}
	d := diff.Diffs[0]
	if d.Action != "changed" {
		t.Errorf("Action = %q, want %q", d.Action, "changed")
	}
	if len(d.ChangedKeys) != 1 || d.ChangedKeys[0] != "instance_type" {
		t.Errorf("ChangedKeys = %v, want [instance_type]", d.ChangedKeys)
	}
}

func TestDiffStates_Unchanged(t *testing.T) {
	state := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-123"}}]}
	]}`

	diff, err := DiffStates([]byte(state), []byte(state), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	if diff.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1", diff.Unchanged)
	}
	if len(diff.Diffs) != 0 {
		t.Errorf("len(Diffs) = %d, want 0", len(diff.Diffs))
	}
}

func TestDiffStates_Mixed(t *testing.T) {
	from := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-1", "size": "small"}}]},
		{"mode": "managed", "type": "aws_s3_bucket", "name": "logs", "provider": "aws", "instances": [{"attributes": {"id": "logs-bucket"}}]}
	]}`
	to := `{"version": 4, "resources": [
		{"mode": "managed", "type": "aws_instance", "name": "web", "provider": "aws", "instances": [{"attributes": {"id": "i-1", "size": "large"}}]},
		{"mode": "managed", "type": "aws_vpc", "name": "main", "provider": "aws", "instances": [{"attributes": {"id": "vpc-1"}}]}
	]}`

	diff, err := DiffStates([]byte(from), []byte(to), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	if diff.Added != 1 {
		t.Errorf("Added = %d, want 1", diff.Added)
	}
	if diff.Removed != 1 {
		t.Errorf("Removed = %d, want 1", diff.Removed)
	}
	if diff.Changed != 1 {
		t.Errorf("Changed = %d, want 1", diff.Changed)
	}
}

func TestDiffStates_WithModules(t *testing.T) {
	from := `{"version": 4, "resources": [
		{"module": "module.vpc", "mode": "managed", "type": "aws_vpc", "name": "main", "provider": "aws", "instances": [{"attributes": {"cidr": "10.0.0.0/16"}}]}
	]}`
	to := `{"version": 4, "resources": [
		{"module": "module.vpc", "mode": "managed", "type": "aws_vpc", "name": "main", "provider": "aws", "instances": [{"attributes": {"cidr": "10.1.0.0/16"}}]}
	]}`

	diff, err := DiffStates([]byte(from), []byte(to), AttributesFull)
	if err != nil {
		t.Fatalf("DiffStates() error = %v", err)
	}

	if diff.Changed != 1 {
		t.Errorf("Changed = %d, want 1", diff.Changed)
	}
	if diff.Diffs[0].Module != "module.vpc" {
		t.Errorf("Module = %q, want %q", diff.Diffs[0].Module, "module.vpc")
	}
}
