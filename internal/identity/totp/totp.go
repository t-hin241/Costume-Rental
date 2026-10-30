// Package totp implements time-based one-time passwords (RFC 6238) with the
// parameters every authenticator app supports: HMAC-SHA1, 6 digits, 30
// second steps. It also produces single-use recovery codes.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// SecretSize is 160 bits, the RFC 4226 recommendation for HMAC-SHA1.
	SecretSize = 20
	period     = 30
	digits     = 6
	// skew accepts codes one step either side of now for clock drift.
	skew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random shared secret.
func NewSecret() []byte {
	s := make([]byte, SecretSize)
	_, _ = rand.Read(s) // crypto/rand.Read never fails on supported platforms
	return s
}

// EncodeSecret is the base32 form users type into an authenticator app.
func EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

// URI is the otpauth:// URI shown as a QR code.
func URI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", EncodeSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(digits))
	q.Set("period", fmt.Sprint(period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Step is the time step containing t.
func Step(t time.Time) int64 { return t.Unix() / period }

// Code returns the code for a time step.
func Code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", digits, v%1_000_000)
}

// Verify checks code against the steps around now and returns the step it
// matched. Steps at or before lastUsed are refused, so an accepted code
// cannot be replayed.
func Verify(secret []byte, code string, now time.Time, lastUsed int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return 0, false
	}
	cur := Step(now)
	for step := cur - skew; step <= cur+skew; step++ {
		if step <= lastUsed {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(Code(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// RecoveryCodeCount is how many recovery codes an enrolment issues.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns codes such as 7KQ2-M9XA-P3LF-T6WB (80 random
// bits each) for the user to store offline.
func NewRecoveryCodes() []string {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		s := b32.EncodeToString(b) // 16 characters
		codes[i] = s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
	}
	return codes
}

// HashRecoveryCode normalises a typed recovery code (case, dashes, spaces)
// and hashes it for storage and lookup. A fast hash suffices: the code is
// random, not chosen by a person.
func HashRecoveryCode(code string) ([]byte, bool) {
	n := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(n) != 16 {
		return nil, false
	}
	if _, err := b32.DecodeString(n); err != nil {
		return nil, false
	}
	h := sha256.Sum256([]byte(n))
	return h[:], true
}
