package handler

import "testing"

func TestIsValidAPIEndpoint(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"https://A1B2C3.gr7.us-west-2.eks.amazonaws.com", true},
		{"https://kubernetes.default.svc", true},
		{"http://insecure.example.com", false},
		{"", false},
		{"A1B2C3.gr7.us-west-2.eks.amazonaws.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isValidAPIEndpoint(tc.in); got != tc.want {
				t.Errorf("isValidAPIEndpoint(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLooksLikePEM(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid cert", "-----BEGIN CERTIFICATE-----\nMIID...\n-----END CERTIFICATE-----", true},
		{"valid cert with surrounding whitespace", "\n   -----BEGIN CERTIFICATE-----\nMIID...\n-----END CERTIFICATE-----\n", true},
		{"missing end marker", "-----BEGIN CERTIFICATE-----\nMIID...\n", false},
		{"missing begin marker", "MIID...\n-----END CERTIFICATE-----", false},
		{"plain text", "this is not a cert", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikePEM(tc.in); got != tc.want {
				t.Errorf("looksLikePEM(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
