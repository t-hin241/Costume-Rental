package envconfig_test

import (
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/platform/envconfig"
)

func TestDefaultsAndOverrides(t *testing.T) {
	env := envconfig.FromMap(map[string]string{
		"AUTH_STAFF_SESSION_IDLE": "15m",
		"HTTP_TRUSTED_ORIGINS":    " https://a.example , ,https://b.example",
		"HTTP_COOKIE_SECURE":      "false",
	})
	c := env.Auth()
	want := identityapp.DefaultAuthConfig()
	want.Sessions.Staff.Idle = 15 * time.Minute
	if c != want {
		t.Fatalf("auth config = %+v", c)
	}
	if got := env.List("HTTP_TRUSTED_ORIGINS"); len(got) != 2 || got[1] != "https://b.example" {
		t.Fatalf("origins = %q", got)
	}
	if env.Bool("HTTP_COOKIE_SECURE", true) {
		t.Fatal("explicit false must win over the default")
	}
	if err := env.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestReportsEveryBadVariable(t *testing.T) {
	env := envconfig.FromMap(map[string]string{"AUTH_PASSWORD_MIN_LENGTH": "twelve", "AUTH_RESET_TOKEN_TTL": "30"})
	env.Auth()
	env.Required("DATABASE_URL")
	err := env.Err()
	for _, key := range []string{"AUTH_PASSWORD_MIN_LENGTH", "AUTH_RESET_TOKEN_TTL", "DATABASE_URL"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("error %v must name %s", err, key)
		}
	}
}

func TestPrefixes(t *testing.T) {
	env := envconfig.FromMap(map[string]string{"P": "10.0.0.0/8, 192.168.1.7 ,::ffff:172.16.0.1,fd00::/8"})
	got := env.Prefixes("P")
	want := []string{"10.0.0.0/8", "192.168.1.7/32", "172.16.0.1/32", "fd00::/8"}
	if len(got) != len(want) || env.Err() != nil {
		t.Fatalf("prefixes = %v, %v", got, env.Err())
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("prefix %d = %s, want %s", i, got[i], want[i])
		}
	}
	bad := envconfig.FromMap(map[string]string{"P": "10.0.0.0/8,proxy.local"})
	bad.Prefixes("P")
	if bad.Err() == nil {
		t.Fatal("a host name must be refused")
	}
}
