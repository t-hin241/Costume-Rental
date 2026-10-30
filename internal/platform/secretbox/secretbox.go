// Package secretbox encrypts small secrets that must be read back (TOTP
// seeds) with AES-256-GCM before they are stored. The key comes from the
// environment and is never stored next to the data.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// version prefixes every ciphertext so the format or key can be rotated.
const version byte = 1

var ErrDecrypt = errors.New("secretbox: cannot decrypt (wrong key or tampered data)")

// Box seals and opens secrets with one key.
type Box struct{ aead cipher.AEAD }

// FromBase64 builds a box from a base64-encoded 32-byte key, e.g. the
// output of `openssl rand -base64 32`.
func FromBase64(key string) (*Box, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("secretbox: key must be 32 bytes, base64-encoded")
	}
	return New(raw)
}

// New builds a box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secretbox: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. context (e.g. the owning user ID) is
// authenticated but not stored, so a ciphertext copied to another row does
// not open.
func (b *Box) Seal(plaintext []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails on supported platforms
	out := append([]byte{version}, nonce...)
	return b.aead.Seal(out, nonce, plaintext, []byte(context))
}

// Open decrypts a Seal output for the same context.
func (b *Box) Open(ciphertext []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(ciphertext) < 1+n || ciphertext[0] != version {
		return nil, ErrDecrypt
	}
	plain, err := b.aead.Open(nil, ciphertext[1:1+n], ciphertext[1+n:], []byte(context))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
