package argon2id_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"webee_rentor/internal/identity/argon2id"
)

func hasher(t *testing.T, p argon2id.Params) *argon2id.Hasher {
	t.Helper()
	h, err := argon2id.New(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := hasher(t, argon2id.OWASP)
	a, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q is not a PHC argon2id string", a)
	}
	b, _ := h.Hash(ctx, "correct horse battery")
	if a == b {
		t.Fatal("each hash needs its own salt")
	}
	if ok, rehash, err := h.Verify(ctx, "correct horse battery", a); !ok || rehash || err != nil {
		t.Fatalf("verify = %v, %v, %v", ok, rehash, err)
	}
	if ok, _, err := h.Verify(ctx, "correct horse batterY", a); ok || err != nil {
		t.Fatalf("wrong password = %v, %v", ok, err)
	}
}

func TestOutdatedParamsAskForRehash(t *testing.T) {
	ctx := context.Background()
	old, _ := hasher(t, argon2id.OWASP).Hash(ctx, "pw")
	stronger := argon2id.OWASP
	stronger.Iterations = 3
	ok, rehash, err := hasher(t, stronger).Verify(ctx, "pw", old)
	if !ok || !rehash || err != nil {
		t.Fatalf("verify with stronger params = %v, %v, %v", ok, rehash, err)
	}
}

func TestRejectsWeakParamsAndMalformedHashes(t *testing.T) {
	weak := argon2id.OWASP
	weak.MemoryKiB = 1024
	if _, err := argon2id.New(weak, 1); !errors.Is(err, argon2id.ErrWeakParams) {
		t.Fatalf("weak params err = %v", err)
	}
	h := hasher(t, argon2id.OWASP)
	good, _ := h.Hash(context.Background(), "pw")
	parts := strings.Split(good, "$")
	for name, enc := range map[string]string{
		"empty":         "",
		"bcrypt":        "$2a$10$abcdefghijklmnopqrstuv",
		"argon2i":       strings.Replace(good, "argon2id", "argon2i", 1),
		"version":       strings.Replace(good, "v=19", "v=16", 1),
		"trailing junk": strings.Replace(good, "p=1", "p=1x", 1),
		"huge memory":   strings.Replace(good, "m=19456", "m=99999999", 1),
		"bad salt":      strings.Join([]string{"", parts[1], parts[2], parts[3], "!!", parts[5]}, "$"),
	} {
		if _, _, err := h.Verify(context.Background(), "pw", enc); !errors.Is(err, argon2id.ErrMalformedHash) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
