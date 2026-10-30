package secretbox_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"webee_rentor/internal/platform/secretbox"
)

func newBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	b, err := secretbox.FromBase64(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpen(t *testing.T) {
	b := newBox(t)
	c := b.Seal([]byte("seed"), "user-1")
	if bytes.Contains(c, []byte("seed")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	if p, err := b.Open(c, "user-1"); err != nil || string(p) != "seed" {
		t.Fatalf("open = %q, %v", p, err)
	}
	if _, err := b.Open(c, "user-2"); !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("other context err = %v", err)
	}
	if _, err := newBox(t).Open(c, "user-1"); !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("other key err = %v", err)
	}
	c[len(c)-1] ^= 1
	if _, err := b.Open(c, "user-1"); !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("tampered err = %v", err)
	}
}

func TestRejectsBadKeys(t *testing.T) {
	for _, k := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := secretbox.FromBase64(k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
