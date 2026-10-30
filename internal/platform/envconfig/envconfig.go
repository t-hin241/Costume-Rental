// Package envconfig reads the process configuration from environment
// variables. Unset variables fall back to the documented defaults; values
// are never logged, since some (DATABASE_URL) hold secrets.
package envconfig

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"webee_rentor/internal/identity/identityapp"
)

// Reader collects parse errors so all bad variables are reported at once.
type Reader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// New reads from the process environment.
func New() *Reader { return &Reader{lookup: os.LookupEnv} }

// FromMap reads from m, for tests.
func FromMap(m map[string]string) *Reader {
	return &Reader{lookup: func(k string) (string, bool) { v, ok := m[k]; return v, ok }}
}

// Err returns every parse error seen so far.
func (r *Reader) Err() error { return errors.Join(r.errs...) }

func (r *Reader) raw(key string) (string, bool) {
	v, ok := r.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// Required returns a variable that must be set.
func (r *Reader) Required(key string) string {
	v, ok := r.raw(key)
	if !ok {
		r.errs = append(r.errs, fmt.Errorf("%s is not set", key))
	}
	return v
}

// String returns a variable or def.
func (r *Reader) String(key, def string) string {
	if v, ok := r.raw(key); ok {
		return v
	}
	return def
}

// List returns a comma-separated variable, or nil.
func (r *Reader) List(key string) []string {
	v, ok := r.raw(key)
	if !ok {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Bool returns a boolean variable or def.
func (r *Reader) Bool(key string, def bool) bool {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s must be true or false", key))
		return def
	}
	return b
}

// Int returns an integer variable or def.
func (r *Reader) Int(key string, def int) int {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s must be an integer", key))
		return def
	}
	return n
}

// Duration returns a Go duration variable (e.g. "30m", "168h") or def.
func (r *Reader) Duration(key string, def time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s must be a duration such as 30m or 168h", key))
		return def
	}
	return d
}

// Auth reads the authentication settings over identityapp.DefaultAuthConfig.
// The result is validated by identityapp.NewAuth.
func (r *Reader) Auth() identityapp.AuthConfig {
	c := identityapp.DefaultAuthConfig()
	c.Credentials.MinPasswordLength = r.Int("AUTH_PASSWORD_MIN_LENGTH", c.Credentials.MinPasswordLength)
	c.Sessions.Customer.TTL = r.Duration("AUTH_CUSTOMER_SESSION_TTL", c.Sessions.Customer.TTL)
	c.Sessions.Customer.Idle = r.Duration("AUTH_CUSTOMER_SESSION_IDLE", c.Sessions.Customer.Idle)
	c.Sessions.Staff.TTL = r.Duration("AUTH_STAFF_SESSION_TTL", c.Sessions.Staff.TTL)
	c.Sessions.Staff.Idle = r.Duration("AUTH_STAFF_SESSION_IDLE", c.Sessions.Staff.Idle)
	c.ResetTokenTTL = r.Duration("AUTH_RESET_TOKEN_TTL", c.ResetTokenTTL)
	c.VerifyTokenTTL = r.Duration("AUTH_EMAIL_VERIFY_TTL", c.VerifyTokenTTL)
	c.ChallengeTTL = r.Duration("AUTH_MFA_CHALLENGE_TTL", c.ChallengeTTL)
	c.MFAIssuer = r.String("AUTH_MFA_ISSUER", c.MFAIssuer)
	return c
}

// Prefixes returns a comma-separated list of CIDR prefixes or single IP
// addresses (taken as /32 or /128), or nil.
func (r *Reader) Prefixes(key string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range r.List(key) {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			r.errs = append(r.errs, fmt.Errorf("%s: %q is not an IP address or CIDR prefix", key, s))
			continue
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out
}
