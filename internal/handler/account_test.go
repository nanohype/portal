package handler

import (
	"strings"
	"testing"
)

func TestIsValidAWSAccountID(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"111111111111", true},
		{"000000000000", true},
		{"12345", false},         // too short
		{"1234567890123", false}, // too long
		{"abcdefghijkl", false},  // not digits
		{"", false},
		{"3516-1975-9866", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isValidAWSAccountID(tc.in); got != tc.want {
				t.Errorf("isValidAWSAccountID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsValidRoleARN(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"arn:aws:iam::111111111111:role/portal-cross-account", true},
		{"arn:aws:iam::111111111111:role/path/role-name", true},
		{"arn:aws-us-gov:iam::111111111111:role/gov-role", true},
		{"arn:aws-cn:iam::111111111111:role/cn-role", true},
		{"arn:aws:iam::abc:role/foo", false},          // non-digit account
		{"arn:aws:iam:::role/no-account", false},      // missing account
		{"arn:aws:iam::111111111111:user/foo", false}, // wrong resource type
		{"arn:aws:iam::111111111111:role/", false},    // empty role name
		{"111111111111:role/foo", false},              // no arn prefix
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isValidRoleARN(tc.in); got != tc.want {
				t.Errorf("isValidRoleARN(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsValidRegion(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"us-west-2", true},
		{"us-east-1", true},
		{"eu-central-1", true},
		{"ap-northeast-1", true},
		{"af-south-1", true},
		{"USWest2", false},
		{"us_west_2", false},
		{"us-west", false}, // no trailing digit
		{"us-west-", false},
		{"", false},
		{"us-west-2a", false}, // AZ, not region
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isValidRegion(tc.in); got != tc.want {
				t.Errorf("isValidRegion(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// accountIDFromARN powers the cross-field validation that an assume-role ARN's
// account portion matches the aws_account_id submitted with it. Mistakes here
// (copying the wrong ARN, fat-fingering a digit) are exactly the kind of input
// errors operators make at 11pm.
func TestAccountIDFromARN(t *testing.T) {
	tests := []struct {
		arn  string
		want string
	}{
		{"arn:aws:iam::111111111111:role/portal", "111111111111"},
		{"arn:aws-us-gov:iam::123456789012:role/gov", "123456789012"},
		{"arn:aws:iam::111111111111:role/with/path/role", "111111111111"},
		{"arn:aws:s3:::bucket-not-a-role", ""},
		{"not an arn", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.arn, func(t *testing.T) {
			if got := accountIDFromARN(tc.arn); got != tc.want {
				t.Errorf("accountIDFromARN(%q) = %q, want %q", tc.arn, got, tc.want)
			}
		})
	}
}

// Sanity check that ARN-account cross-field matching catches the most common
// mistake: pasted the wrong role ARN, account portion no longer matches.
func TestCrossFieldARNAccountMatch(t *testing.T) {
	const submittedAccount = "111111111111"
	cases := []struct {
		name        string
		arn         string
		shouldMatch bool
	}{
		{"matches", "arn:aws:iam::111111111111:role/portal", true},
		{"mismatched account", "arn:aws:iam::999999999999:role/portal", false},
		{"off by one digit", "arn:aws:iam::111111111112:role/portal", false},
		{"junk arn", "not-an-arn", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := accountIDFromARN(tc.arn) == submittedAccount
			if got != tc.shouldMatch {
				t.Errorf("match(%q, %q) = %v, want %v", tc.arn, submittedAccount, got, tc.shouldMatch)
			}
		})
	}
}

// checkSubstrateARNs is the registration-time half of a rule clusterspec.Input
// also enforces at order time. Refusing here is what keeps an account from
// looking correctly registered until its first cross-account vend is rejected.
func TestCheckSubstrateARNs(t *testing.T) {
	const (
		role      = "arn:aws:iam::222222222222:role/production-fleet-vend"
		kms       = "arn:aws:kms:us-west-2:222222222222:key/abcd-1234"
		clusterPB = "arn:aws:iam::222222222222:policy/production-cluster-boundary"
		operPB    = "arn:aws:iam::222222222222:policy/production-operator-boundary"
	)

	cases := []struct {
		name                         string
		role, kms, clusterPB, operPB string
		wantSubstring                string
	}{
		{name: "an account with no prerequisites is fine — same-account vends need none"},
		{name: "a data key alone is fine", kms: kms},
		{name: "the full cross-account set", role: role, kms: kms, clusterPB: clusterPB, operPB: operPB},
		{name: "boundaries without a role are fine (ungated, but harmless)", clusterPB: clusterPB, operPB: operPB},

		{name: "a vend role with neither boundary", role: role,
			wantSubstring: "requires cluster_permissions_boundary_arn and operator_permissions_boundary_arn"},
		{name: "a vend role missing the operator boundary", role: role, clusterPB: clusterPB,
			wantSubstring: "requires cluster_permissions_boundary_arn"},
		{name: "a vend role missing the cluster boundary", role: role, operPB: operPB,
			wantSubstring: "requires cluster_permissions_boundary_arn"},

		{name: "a role ARN that is not an ARN", role: "production-fleet-vend", clusterPB: clusterPB, operPB: operPB,
			wantSubstring: "vend_role_arn must be an IAM or KMS ARN"},
		{name: "a boundary pointing at a non-AWS service", role: role, clusterPB: "arn:aws:s3:::bucket/key", operPB: operPB,
			wantSubstring: "cluster_permissions_boundary_arn must be an IAM or KMS ARN"},
		{name: "an ARN with a short account id", kms: "arn:aws:kms:us-west-2:2222:key/abcd",
			wantSubstring: "data_kms_key_arn must be an IAM or KMS ARN"},
		{name: "an over-long ARN", kms: "arn:aws:kms:us-west-2:222222222222:key/" + strings.Repeat("x", 2048),
			wantSubstring: "data_kms_key_arn must be at most 2048 characters"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkSubstrateARNs(tc.role, tc.kms, tc.clusterPB, tc.operPB)
			if tc.wantSubstring == "" {
				if got != "" {
					t.Errorf("rejected a valid combination: %s", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("accepted a combination that should have been refused")
			}
			if !strings.Contains(got, tc.wantSubstring) {
				t.Errorf("refused for the wrong reason:\n  want substring: %s\n  got: %s", tc.wantSubstring, got)
			}
		})
	}
}
