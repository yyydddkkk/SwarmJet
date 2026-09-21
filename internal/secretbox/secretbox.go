// Package secretbox encrypts GitLab credentials at rest. The master key is
// supplied by deployment configuration and is never stored in the database, so
// key material and ciphertext are managed separately. A stable key is required:
// generating a fresh key on startup would make previously saved tokens
// undecryptable.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Box seals and opens short credential payloads with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// ParseKey accepts a 32-byte key encoded as hex or standard/URL-safe base64.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("master key is empty")
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(raw); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("master key must be 32 bytes encoded as hex or base64")
}

func New(raw string) (*Box, error) {
	key, err := ParseKey(raw)
	if err != nil {
		return nil, fmt.Errorf("SWARMJET_MASTER_KEY: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns a URL-safe nonce||ciphertext string. It never includes the key.
func (b *Box) Seal(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

// Open decrypts a value produced by Seal, or reports an opaque corruption error.
func (b *Box) Open(encoded string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("invalid encrypted credential")
	}
	size := b.aead.NonceSize()
	if len(raw) < size {
		return "", errors.New("invalid encrypted credential")
	}
	plain, err := b.aead.Open(nil, raw[:size], raw[size:], nil)
	if err != nil {
		return "", errors.New("unable to decrypt credential; check the persisted master key")
	}
	return string(plain), nil
}

// Fingerprint identifies a token without revealing it, so credential rotation
// can be observed and audit records stay secret-free.
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:6])
}
