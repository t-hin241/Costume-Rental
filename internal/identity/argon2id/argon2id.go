// Package argon2id hashes passwords with Argon2id and encodes them in the
// PHC string format, e.g. $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
// Each hash has its own random salt; the parameters travel with the hash so
// they can be raised later and old hashes upgraded on the next login.
package argon2id

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// OWASP is the OWASP Password Storage Cheat Sheet minimum for Argon2id
// (19 MiB, 2 iterations, 1 lane). Benchmark on the production host and raise
// it when the latency budget allows.
var OWASP = Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}

var (
	ErrMalformedHash = errors.New("argon2id: malformed hash")
	ErrWeakParams    = errors.New("argon2id: parameters below the OWASP minimum")
)

// Hasher hashes and verifies passwords. Each hash holds MemoryKiB of RAM,
// so at most maxConcurrent run at once; the rest wait or give up with
// their context.
type Hasher struct {
	params Params
	slots  chan struct{}
}

// New builds a hasher. Parameters below the OWASP minimum are refused.
func New(p Params, maxConcurrent int) (*Hasher, error) {
	if p.MemoryKiB < OWASP.MemoryKiB || p.Iterations < OWASP.Iterations || p.Parallelism < 1 ||
		p.SaltLength < 16 || p.KeyLength < 32 {
		return nil, ErrWeakParams
	}
	if maxConcurrent < 1 {
		return nil, errors.New("argon2id: maxConcurrent must be positive")
	}
	return &Hasher{params: p, slots: make(chan struct{}, maxConcurrent)}, nil
}

// Hash returns the encoded hash of password with a fresh salt.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2id: salt: %w", err)
	}
	key, err := h.derive(ctx, password, salt, h.params)
	if err != nil {
		return "", err
	}
	return encode(h.params, salt, key), nil
}

// Verify reports whether password matches encoded, in constant time, and
// whether the hash should be replaced because its parameters are outdated.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, rehash bool, err error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	got, err := h.derive(ctx, password, salt, p)
	if err != nil {
		return false, false, err
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, p != h.params, nil
}

func (h *Hasher) derive(ctx context.Context, password string, salt []byte, p Params) ([]byte, error) {
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("argon2id: waiting for a hashing slot: %w", ctx.Err())
	}
	defer func() { <-h.slots }()
	return argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLength), nil
}

var b64 = base64.RawStdEncoding

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// maxDecodedMemoryKiB caps the cost a stored hash can demand (1 GiB), so a
// tampered hash cannot exhaust memory.
const maxDecodedMemoryKiB = 1 << 20

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}
	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	// Re-encoding must give back the same text, which rejects trailing junk.
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil ||
		fmt.Sprintf("m=%d,t=%d,p=%d", p.MemoryKiB, p.Iterations, p.Parallelism) != parts[3] ||
		p.MemoryKiB == 0 || p.MemoryKiB > maxDecodedMemoryKiB || p.Iterations == 0 || p.Iterations > 100 || p.Parallelism == 0 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	p.SaltLength, p.KeyLength = uint32(len(salt)), uint32(len(key))
	return p, salt, key, nil
}
