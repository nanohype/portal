package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Encryptor handles AES-256-GCM encryption for sensitive values.
type Encryptor struct {
	gcm cipher.AEAD
	key []byte
}

func NewEncryptor(key string) (*Encryptor, error) {
	keyBytes := []byte(key)
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(keyBytes))
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		// Unreachable: aes.NewCipher only rejects a key that is not 16/24/32
		// bytes, and the length check above already returned for anything but 32.
		return nil, fmt.Errorf("failed to create cipher: %w", err) //coverage:ignore unreachable — key length validated above
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		// Unreachable: NewGCM only rejects a block whose size is not 16, and AES
		// is always 16.
		return nil, fmt.Errorf("failed to create GCM: %w", err) //coverage:ignore unreachable — AES block size is always 16
	}

	return &Encryptor{gcm: gcm, key: keyBytes}, nil
}

func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := e.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (e *Encryptor) Decrypt(encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode: %w", err)
	}

	nonceSize := e.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

// DerivePassphrase derives a deterministic passphrase for a given scope
// (e.g. workspace ID) using HMAC-SHA256 with the encryption key.
// Used to generate per-workspace OpenTofu state encryption passphrases.
func (e *Encryptor) DerivePassphrase(scope string) string {
	mac := hmac.New(sha256.New, e.key)
	mac.Write([]byte(scope))
	return hex.EncodeToString(mac.Sum(nil))
}
