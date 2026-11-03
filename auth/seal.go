package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

var errBadSeal = errors.New("invalid or tampered cookie")

// sealer encrypts and authenticates cookie values with AES-GCM, so sessions
// need no server-side state and work across replicas sharing the secret.
type sealer struct {
	aead cipher.AEAD
}

func newSealer(secret string) (*sealer, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

// seal encodes v for a cookie. purpose is bound into the ciphertext so a
// value sealed for one cookie cannot be replayed as another.
func (s *sealer) seal(purpose string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+len(plain)+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(purpose))), nil
}

// open decodes a value produced by seal with the same purpose into v.
func (s *sealer) open(purpose, value string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) < s.aead.NonceSize() {
		return errBadSeal
	}
	plain, err := s.aead.Open(nil, b[:s.aead.NonceSize()], b[s.aead.NonceSize():], []byte(purpose))
	if err != nil {
		return errBadSeal
	}
	return json.Unmarshal(plain, v)
}
