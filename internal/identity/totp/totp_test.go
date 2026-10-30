package totp_test

import (
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/identity/totp"
)

// RFC 6238 appendix B, SHA-1 secret; the 6-digit code is the last six
// digits of the 8-digit test value.
var rfcSecret = []byte("12345678901234567890")

func TestRFC6238Vectors(t *testing.T) {
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		if got := totp.Code(rfcSecret, totp.Step(time.Unix(unix, 0))); got != want {
			t.Errorf("T=%d: code %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyAllowsOneStepOfDriftAndRefusesReplay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	step := totp.Step(now)
	secret := totp.NewSecret()
	for _, d := range []int64{-1, 0, 1} {
		if got, ok := totp.Verify(secret, totp.Code(secret, step+d), now, 0); !ok || got != step+d {
			t.Errorf("drift %d: %d, %v", d, got, ok)
		}
	}
	if _, ok := totp.Verify(secret, totp.Code(secret, step+2), now, 0); ok {
		t.Error("a code two steps ahead must be refused")
	}
	if _, ok := totp.Verify(secret, totp.Code(secret, step), now, step); ok {
		t.Error("a code for an already used step must be refused")
	}
	if _, ok := totp.Verify(secret, "12345", now, 0); ok {
		t.Error("short code accepted")
	}
}

func TestURI(t *testing.T) {
	u := totp.URI("Webee Rentor", "a@example.test", rfcSecret)
	if !strings.HasPrefix(u, "otpauth://totp/Webee%20Rentor:a@example.test?") || !strings.Contains(u, "secret="+totp.EncodeSecret(rfcSecret)) {
		t.Fatalf("uri = %s", u)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := totp.NewRecoveryCodes()
	if len(codes) != totp.RecoveryCodeCount || len(codes[0]) != 19 {
		t.Fatalf("codes = %v", codes)
	}
	a, ok := totp.HashRecoveryCode(codes[0])
	b, ok2 := totp.HashRecoveryCode(" " + strings.ToLower(strings.ReplaceAll(codes[0], "-", "")) + " ")
	if !ok || !ok2 || string(a) != string(b) {
		t.Fatal("typed variants of a code must hash alike")
	}
	if _, ok := totp.HashRecoveryCode("123456"); ok {
		t.Fatal("a TOTP code is not a recovery code")
	}
}
