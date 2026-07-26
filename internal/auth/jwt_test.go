package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTRoundTrip(t *testing.T) {
	j := NewJWTAuth("test-secret", time.Hour)

	tok, err := j.GenerateToken("user-1", "org-1", "a@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	claims, err := j.ValidateToken(tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.UserID != "user-1" || claims.OrgID != "org-1" || claims.Email != "a@example.com" {
		t.Errorf("claims did not round-trip: %+v", claims)
	}
}

// TestValidateToken_RejectsNonHMAC is the algorithm-confusion guard. Without the
// signing-method check in the keyfunc, a token that names a different algorithm
// is verified with the HMAC secret as that algorithm's key material — the classic
// way a JWT verifier is turned into an oracle that accepts attacker-signed
// tokens. The parse must fail before any signature comparison.
func TestValidateToken_RejectsNonHMAC(t *testing.T) {
	j := NewJWTAuth("test-secret", time.Hour)

	// alg=none: no signature at all. jwt.UnsafeAllowNoneSignatureType is the
	// library's explicit opt-in for producing one, which is exactly the shape an
	// attacker submits.
	tok, err := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{
		UserID: "attacker", OrgID: "org-1",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("could not mint the alg=none token the test needs: %v", err)
	}

	if _, err := j.ValidateToken(tok); err == nil {
		t.Fatal("an alg=none token validated; the signing-method check is not enforced")
	}
}

func TestValidateToken_Rejects(t *testing.T) {
	j := NewJWTAuth("test-secret", time.Hour)

	other := NewJWTAuth("a-different-secret", time.Hour)
	foreign, err := other.GenerateToken("user-1", "org-1", "a@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	expired := NewJWTAuth("test-secret", -time.Hour)
	stale, err := expired.GenerateToken("user-1", "org-1", "a@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	for name, tok := range map[string]string{
		"signed with another secret": foreign,
		"expired":                    stale,
		"not a jwt":                  "not-a-token",
		"empty":                      "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := j.ValidateToken(tok); err == nil {
				t.Error("token validated but should not have")
			}
		})
	}
}
