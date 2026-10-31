package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"webee_rentor/internal/platform/ratelimit"
)

// RateLimits protects the endpoints attackers hammer: credential stuffing,
// code guessing, account enumeration and email bombing (decision D23 for
// the original limits, D24 for the verification and MFA ones).
type RateLimits struct {
	LoginPerIP           ratelimit.Rate
	LoginPerEmail        ratelimit.Rate
	RegisterPerIP        ratelimit.Rate
	ResetPerIP           ratelimit.Rate
	ResetPerEmail        ratelimit.Rate
	ResetConfirmIP       ratelimit.Rate
	PasswordPerUser      ratelimit.Rate
	VerifyResendPerIP    ratelimit.Rate
	VerifyResendPerEmail ratelimit.Rate
	VerifyConfirmPerIP   ratelimit.Rate
	MFAPerIP             ratelimit.Rate // second login step and its enrolment
	MFAPerUser           ratelimit.Rate // setup, confirm and disable while signed in
	QuotePerIP           ratelimit.Rate // public availability checks and quotes
}

// DefaultRateLimits returns the approved limits.
func DefaultRateLimits() RateLimits {
	return RateLimits{
		LoginPerIP:           ratelimit.Rate{N: 20, Window: time.Minute},
		LoginPerEmail:        ratelimit.Rate{N: 10, Window: 15 * time.Minute},
		RegisterPerIP:        ratelimit.Rate{N: 10, Window: time.Hour},
		ResetPerIP:           ratelimit.Rate{N: 10, Window: 15 * time.Minute},
		ResetPerEmail:        ratelimit.Rate{N: 3, Window: time.Hour},
		ResetConfirmIP:       ratelimit.Rate{N: 10, Window: 15 * time.Minute},
		PasswordPerUser:      ratelimit.Rate{N: 5, Window: 15 * time.Minute},
		VerifyResendPerIP:    ratelimit.Rate{N: 10, Window: 15 * time.Minute},
		VerifyResendPerEmail: ratelimit.Rate{N: 3, Window: time.Hour},
		VerifyConfirmPerIP:   ratelimit.Rate{N: 10, Window: 15 * time.Minute},
		MFAPerIP:             ratelimit.Rate{N: 20, Window: 15 * time.Minute},
		MFAPerUser:           ratelimit.Rate{N: 10, Window: 15 * time.Minute},
		QuotePerIP:           ratelimit.Rate{N: 120, Window: time.Minute},
	}
}

func (l RateLimits) all() []ratelimit.Rate {
	return []ratelimit.Rate{l.LoginPerIP, l.LoginPerEmail, l.RegisterPerIP, l.ResetPerIP, l.ResetPerEmail, l.ResetConfirmIP,
		l.PasswordPerUser, l.VerifyResendPerIP, l.VerifyResendPerEmail, l.VerifyConfirmPerIP, l.MFAPerIP, l.MFAPerUser, l.QuotePerIP}
}

// LongestWindow is how long counters must be kept.
func (l RateLimits) LongestWindow() (d time.Duration) {
	for _, r := range l.all() {
		d = max(d, r.Window)
	}
	return d
}

func (l RateLimits) validate() error {
	for _, r := range l.all() {
		if !r.Valid() {
			return errors.New("httpapi: every rate limit must be positive")
		}
	}
	return nil
}

// limit counts a hit for bucket:key and returns a rate-limit error when
// over r.
func (s *Server) limit(r *http.Request, bucket, key string, rate ratelimit.Rate) error {
	ok, retry, err := s.cfg.Limiter.Allow(r.Context(), bucket+":"+key, rate)
	if err != nil {
		return err
	}
	if !ok {
		return errRateLimited(retry)
	}
	return nil
}

// clientIP is the TCP peer address, or, when the peer is a trusted proxy,
// the right-most X-Forwarded-For entry that is not itself a trusted proxy.
// Entries further left are client-supplied and never believed.
func (s *Server) clientIP(r *http.Request) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			return r.RemoteAddr
		}
		return host
	}
	ip := peer.Addr().Unmap()
	if !s.trustedProxy(ip) {
		return ip.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // malformed: stop at the last address we can trust
		}
		hop = hop.Unmap()
		if !s.trustedProxy(hop) {
			return hop.String()
		}
		ip = hop
	}
	return ip.String()
}

func (s *Server) trustedProxy(ip netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// emailKey normalises an email for rate-limit keys without validating it.
func emailKey(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if len(e) > 320 {
		e = e[:320]
	}
	return e
}
