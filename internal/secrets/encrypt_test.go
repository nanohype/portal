package secrets

import (
	"encoding/base64"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef" // exactly 32 bytes

func TestEncryptDecryptRoundTrip(t *testing.T) {
	e, err := NewEncryptor(testKey)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	for _, plaintext := range []string{"", "hunter2", "a much longer secret value with spaces and symbols !@#$%^&*()", "unicode: café ☕ 日本語"} {
		ct, err := e.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		got, err := e.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != plaintext {
			t.Errorf("round-trip = %q, want %q", got, plaintext)
		}
	}
}

func TestDecryptTamperedCiphertextFails(t *testing.T) {
	e, _ := NewEncryptor(testKey)
	ct, _ := e.Encrypt("sensitive")
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Flip a byte in the ciphertext body (past the 12-byte GCM nonce) — the
	// auth tag must reject it rather than return garbage plaintext.
	if len(raw) <= 12 {
		t.Fatalf("ciphertext unexpectedly short: %d bytes", len(raw))
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := e.Decrypt(tampered); err == nil {
		t.Error("Decrypt of tampered ciphertext returned nil error; GCM auth tag should have rejected it")
	}
}

func TestNewEncryptorRejectsWrongKeySize(t *testing.T) {
	for _, key := range []string{"", "short", strings.Repeat("x", 16), strings.Repeat("x", 31), strings.Repeat("x", 33), strings.Repeat("x", 64)} {
		if _, err := NewEncryptor(key); err == nil {
			t.Errorf("NewEncryptor(%d-byte key) = nil error, want rejection", len(key))
		}
	}
}

func TestDecryptShortCiphertext(t *testing.T) {
	e, _ := NewEncryptor(testKey)
	// A valid base64 blob shorter than the GCM nonce must be rejected, not panic.
	short := base64.StdEncoding.EncodeToString([]byte("tiny"))
	_, err := e.Decrypt(short)
	if err == nil || !strings.Contains(err.Error(), "ciphertext too short") {
		t.Errorf("Decrypt(short) err = %v, want \"ciphertext too short\"", err)
	}
	// Non-base64 input is rejected at the decode step.
	if _, err := e.Decrypt("not valid base64!!!"); err == nil {
		t.Error("Decrypt of non-base64 returned nil error")
	}
}

func TestDerivePassphraseDeterministicAndScoped(t *testing.T) {
	e1, _ := NewEncryptor(testKey)
	e2, _ := NewEncryptor(testKey) // separate instance, same key

	a1 := e1.DerivePassphrase("state:workspace-a")
	a2 := e1.DerivePassphrase("state:workspace-a")
	if a1 != a2 {
		t.Error("DerivePassphrase not deterministic across calls on the same instance")
	}
	if a1 != e2.DerivePassphrase("state:workspace-a") {
		t.Error("DerivePassphrase differs across instances built from the same key")
	}
	if a1 == e1.DerivePassphrase("state:workspace-b") {
		t.Error("DerivePassphrase collides across distinct scopes")
	}
	if a1 == "" {
		t.Error("DerivePassphrase returned empty string")
	}
}

func TestEncryptUsesRandomNonce(t *testing.T) {
	e, _ := NewEncryptor(testKey)
	ct1, _ := e.Encrypt("same-plaintext")
	ct2, _ := e.Encrypt("same-plaintext")
	if ct1 == ct2 {
		t.Error("two encryptions of the same plaintext produced identical ciphertext; nonce is not random")
	}
}
