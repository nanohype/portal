package handler

import "testing"

func TestIsValidAWSAccountID(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"351619759866", true},
		{"000000000000", true},
		{"12345", false},      // too short
		{"1234567890123", false}, // too long
		{"abcdefghijkl", false}, // not digits
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
		{"arn:aws:iam::351619759866:role/tofui-cross-account", true},
		{"arn:aws:iam::351619759866:role/path/role-name", true},
		{"arn:aws-us-gov:iam::351619759866:role/gov-role", true},
		{"arn:aws-cn:iam::351619759866:role/cn-role", true},
		{"arn:aws:iam::abc:role/foo", false},      // non-digit account
		{"arn:aws:iam:::role/no-account", false},  // missing account
		{"arn:aws:iam::351619759866:user/foo", false}, // wrong resource type
		{"arn:aws:iam::351619759866:role/", false},    // empty role name
		{"351619759866:role/foo", false},              // no arn prefix
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
		{"arn:aws:iam::351619759866:role/tofui", "351619759866"},
		{"arn:aws-us-gov:iam::123456789012:role/gov", "123456789012"},
		{"arn:aws:iam::351619759866:role/with/path/role", "351619759866"},
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
	const submittedAccount = "351619759866"
	cases := []struct {
		name      string
		arn       string
		shouldMatch bool
	}{
		{"matches", "arn:aws:iam::351619759866:role/tofui", true},
		{"mismatched account", "arn:aws:iam::999999999999:role/tofui", false},
		{"off by one digit", "arn:aws:iam::351619759867:role/tofui", false},
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
