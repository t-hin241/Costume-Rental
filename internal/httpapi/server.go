// Package httpapi is the HTTP transport: routing, middleware, request DTOs
// and the uniform error format. Handlers parse and validate input, call
// a use case with the principal resolved from the session cookie, and
// write the response; business rules stay in the domain packages.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/ratelimit"
)

// Config holds transport settings.
type Config struct {
	// CookieSecure marks the session cookie Secure and enables HSTS. It
	// must be true in production (HTTPS only); false is for local HTTP.
	CookieSecure bool
	// TrustedOrigins are other origins of the same site (e.g. the
	// frontend https://shop.example.vn for the API https://api.shop.example.vn)
	// allowed to call the API with credentials: CORS allowlist and CSRF
	// trust. Empty = same origin only.
	TrustedOrigins []string
	// TrustedProxies are reverse proxies whose X-Forwarded-For is believed
	// for the client address. Empty = use the TCP peer address.
	TrustedProxies []netip.Prefix
	RequestTimeout time.Duration
	RateLimits     RateLimits
	// Limiter stores rate-limit counters (ratelimitpg in production, so
	// every instance shares them).
	Limiter ratelimit.Limiter
	// Now replaces time.Now for cookie lifetimes, in tests.
	Now func() time.Time
}

// Server routes requests to the use cases.
type Server struct {
	cfg      Config
	identity *identityapp.Service
	auth     *identityapp.Auth
	ready    func(context.Context) error
	origins  map[string]bool
}

// New builds the HTTP handler. ready reports whether dependencies (the
// database) are reachable, for the readiness probe.
func New(cfg Config, svc *identityapp.Service, auth *identityapp.Auth, ready func(context.Context) error) (http.Handler, error) {
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("httpapi: RequestTimeout must be positive")
	}
	if cfg.Limiter == nil {
		return nil, errors.New("httpapi: a rate limiter is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := cfg.RateLimits.validate(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, identity: svc, auth: auth, ready: ready, origins: map[string]bool{}}

	// CSRF: browsers send Sec-Fetch-Site/Origin, and cross-origin unsafe
	// requests are refused unless the origin is trusted. Together with the
	// SameSite=Lax cookie this protects every state-changing endpoint,
	// including login.
	csrf := http.NewCrossOriginProtection()
	for _, o := range cfg.TrustedOrigins {
		if err := csrf.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("httpapi: trusted origin %q: %w", o, err)
		}
		s.origins[o] = true
	}
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errCrossOrigin) }))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("POST /api/v1/auth/verify-email", s.verifyEmail)
	mux.HandleFunc("POST /api/v1/auth/verify-email/resend", s.resendVerification)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/login/mfa/enroll", s.loginEnroll)
	mux.HandleFunc("POST /api/v1/auth/login/mfa", s.loginMFA)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/me", s.authed(s.me))
	mux.HandleFunc("POST /api/v1/auth/password", s.authed(s.changePassword))
	mux.HandleFunc("POST /api/v1/auth/password-reset", s.requestPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/password-reset/confirm", s.confirmPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/mfa/setup", s.authed(s.mfaSetup))
	mux.HandleFunc("POST /api/v1/auth/mfa/setup/confirm", s.authed(s.mfaSetupConfirm))
	mux.HandleFunc("POST /api/v1/auth/mfa/disable", s.authed(s.mfaDisable))

	mux.HandleFunc("GET /api/v1/admin/users/{id}", s.authed(s.adminUser))
	mux.HandleFunc("PUT /api/v1/admin/users/{id}/roles/{role}", s.authed(s.grantRole))
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}/roles/{role}", s.authed(s.revokeRole))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/lock", s.authed(s.lockUser))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/unlock", s.authed(s.unlockUser))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/mfa-reset", s.authed(s.resetMFA))
	mux.HandleFunc("GET /api/v1/admin/permission-matrix", s.authed(s.permissionMatrix))
	mux.HandleFunc("POST /api/v1/admin/permission-matrix", s.authed(s.publishPermissionMatrix))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errNotFound) })

	var h http.Handler = mux
	h = csrf.Handler(h)
	h = s.cors(h)
	h = s.withTimeout(h)
	h = s.securityHeaders(h)
	h = recoverAndLog(h)
	h = withRequestID(h)
	return h, nil
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports only up or down; dependency errors go to the log.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		if err := s.ready(r.Context()); err != nil {
			slog.WarnContext(r.Context(), "not ready", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// authedHandler receives the authenticated principal and its session.
type authedHandler func(w http.ResponseWriter, r *http.Request, p identity.Principal, sess identity.Session)

// authed resolves the session cookie into a principal (recomputed per
// request) or answers 401. Endpoints still check their own permissions
// through the use case.
func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cookieName())
		if err != nil {
			writeError(w, r, identity.ErrNotPrincipal)
			return
		}
		p, sess, err := s.auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			if errors.Is(err, identity.ErrNotPrincipal) {
				s.clearSessionCookie(w)
			}
			writeError(w, r, err)
			return
		}
		h(w, r.WithContext(withUser(r.Context(), p.UserID)), p, sess)
	}
}

func (s *Server) withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		if s.cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const (
	requestIDKey ctxKey = iota
	userIDKey
)

// withRequestID gives every request a fresh ID, returned in X-Request-ID
// and attached to its log lines. Client-supplied IDs are not trusted.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ids.New()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func withUser(ctx context.Context, id identity.UserID) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// recoverAndLog logs one line per request (no bodies, no query strings) and
// turns a panic into a 500 without leaking the stack to the client.
func recoverAndLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "panic", "value", fmt.Sprint(v), "stack", string(debug.Stack()))
				if rec.status == 0 {
					writeError(rec, r, errInternal)
				}
			}
			slog.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

// LogHandler wraps a slog handler to add the request and user IDs carried
// by the context to every record.
func LogHandler(h slog.Handler) slog.Handler { return ctxLogHandler{h} }

type ctxLogHandler struct{ slog.Handler }

func (h ctxLogHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id := requestID(ctx); id != "" {
		rec.AddAttrs(slog.String("request_id", id))
	}
	if id, ok := ctx.Value(userIDKey).(identity.UserID); ok && id != "" {
		rec.AddAttrs(slog.String("user_id", string(id)))
	}
	return h.Handler.Handle(ctx, rec)
}

func (h ctxLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxLogHandler{h.Handler.WithAttrs(attrs)}
}

func (h ctxLogHandler) WithGroup(name string) slog.Handler {
	return ctxLogHandler{h.Handler.WithGroup(name)}
}
