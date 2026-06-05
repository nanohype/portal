package service

import (
	"testing"

	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/secrets"
)

func newTestEncryptor(t *testing.T) *secrets.Encryptor {
	t.Helper()
	enc, err := secrets.NewEncryptor("0123456789abcdef0123456789abcdef") // 32 bytes
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

// Decrypt must recover the CA + token for a sa_token cluster.
func TestClusterDecrypt_SAToken(t *testing.T) {
	enc := newTestEncryptor(t)
	svc := NewClusterService(nil, nil, enc)

	caEnc, _ := enc.Encrypt("-----BEGIN CERTIFICATE-----\nMIID\n-----END CERTIFICATE-----")
	tokEnc, _ := enc.Encrypt("sa-bearer-token")

	creds, err := svc.Decrypt(repository.Cluster{
		AuthMode:          AuthModeSAToken,
		APIEndpoint:       "https://eks.example",
		CABundleEncrypted: caEnc,
		SATokenEncrypted:  tokEnc,
	})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if creds.SAToken != "sa-bearer-token" {
		t.Errorf("SAToken = %q, want sa-bearer-token", creds.SAToken)
	}
	if len(creds.CABundle) == 0 {
		t.Error("CABundle should be populated")
	}
}

// Decrypt must NOT attempt to decrypt the (empty/absent) token for an eks_iam
// cluster — those clusters store no token; the worker mints one per request.
// A stale or garbage SATokenEncrypted must be ignored, not surfaced as an error.
func TestClusterDecrypt_EKSIAMSkipsToken(t *testing.T) {
	enc := newTestEncryptor(t)
	svc := NewClusterService(nil, nil, enc)

	caEnc, _ := enc.Encrypt("-----BEGIN CERTIFICATE-----\nMIID\n-----END CERTIFICATE-----")

	for _, tokenCol := range []string{"", "not-valid-ciphertext"} {
		creds, err := svc.Decrypt(repository.Cluster{
			AuthMode:          AuthModeEKSIAM,
			APIEndpoint:       "https://eks.example",
			CABundleEncrypted: caEnc,
			SATokenEncrypted:  tokenCol,
		})
		if err != nil {
			t.Fatalf("Decrypt(eks_iam, token=%q): unexpected error %v", tokenCol, err)
		}
		if creds.SAToken != "" {
			t.Errorf("eks_iam SAToken = %q, want empty", creds.SAToken)
		}
		if len(creds.CABundle) == 0 {
			t.Error("CABundle should still be decrypted for eks_iam")
		}
	}
}
