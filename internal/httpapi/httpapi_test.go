package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"webee_rentor/internal/backoffice"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog/catalogapp"
	"webee_rentor/internal/httpapi"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/argon2id"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/identity/totp"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/platform/ratelimit"
	"webee_rentor/internal/platform/secretbox"
	"webee_rentor/internal/rentals/rentalsapp"
	"webee_rentor/internal/support/supportapp"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	pgtest.Main(m, pgtest.SeedConfig)
}

const password = "correct horse battery"

// outbox keeps the latest verification token per email.
type outbox struct {
	mu     sync.Mutex
	verify map[string]string
}

func (o *outbox) VerifyEmail(_ context.Context, email, token string, _ time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.verify[email] = token
	return nil
}
func (o *outbox) PasswordReset(context.Context, string, string, time.Time) error { return nil }
func (o *outbox) AccountExists(context.Context, string) error                    { return nil }

type api struct {
	t       *testing.T
	f       *pgtest.Fixture
	h       http.Handler
	mail    *outbox
	secrets map[string][]byte
	// shop drives the business services; moving it leaves sessions alone.
	shop *pgtest.Clock
}

type options struct {
	limits  httpapi.RateLimits
	origins []string
	proxies []netip.Prefix
}

func newAPI(t *testing.T, o options) *api {
	t.Helper()
	if o.limits == (httpapi.RateLimits{}) {
		o.limits = httpapi.DefaultRateLimits()
	}
	f := pgtest.New(t)
	clock := pgtest.NewClock(pgtest.Now)
	svc := identityapp.New(f.Pool, identityapp.WithClock(clock.Now))
	hasher, err := argon2id.New(argon2id.OWASP, 4)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secretbox.New(key)
	mail := &outbox{verify: map[string]string{}}
	auth, err := identityapp.NewAuth(svc, identityapp.DefaultAuthConfig(), hasher, mail, box)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	shop := pgtest.NewClock(pgtest.Now)
	stock := inventoryapp.New(f.Pool, inventoryapp.WithClock(shop.Now))
	bookings := rentalsapp.New(f.Pool, loc, rentalsapp.WithClock(shop.Now))
	h, err := httpapi.New(httpapi.Config{
		CookieSecure: true, RequestTimeout: 10 * time.Second, RateLimits: o.limits, Now: clock.Now,
		Limiter: ratelimit.NewMemory(clock.Now), TrustedOrigins: o.origins, TrustedProxies: o.proxies,
		Services: &httpapi.Services{
			Catalog: catalogapp.New(f.Pool, catalogapp.WithClock(shop.Now)), Rentals: bookings, Inventory: stock,
			Support: supportapp.New(f.Pool, supportapp.WithClock(shop.Now)), Backoffice: backoffice.New(f.Pool, loc, bookings, stock, backoffice.WithClock(shop.Now)),
			Store: httpapi.StoreInfo{Name: "Cửa hàng thử", Address: "1 Đường Thử", Phone: "0900000000", Email: "shop@example.test", Hours: "9:00 - 18:00"},
		},
	}, svc, auth, func(ctx context.Context) error { return f.Pool.Ping(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	return &api{t: t, f: f, h: h, mail: mail, secrets: map[string][]byte{}, shop: shop}
}

type call struct {
	method, path, body string
	cookie             *http.Cookie
	header             map[string]string
	remote             string
}

func (a *api) do(c call) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(c.method, "https://api.example.test"+c.path, strings.NewReader(c.body))
	if c.body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.header {
		r.Header.Set(k, v)
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	if c.remote != "" {
		r.RemoteAddr = c.remote
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	return w
}

func (a *api) expect(w *httptest.ResponseRecorder, status int, code string) {
	a.t.Helper()
	if w.Code != status {
		a.t.Fatalf("status = %d, want %d: %s", w.Code, status, w.Body)
	}
	if code == "" {
		return
	}
	var body struct {
		Error     struct{ Code string }
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != code || body.RequestID == "" {
		a.t.Fatalf("error body = %s, want code %s with a request id", w.Body, code)
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

func creds(email, pw string) string { return `{"email":"` + email + `","password":"` + pw + `"}` }

func (a *api) register(email string) {
	a.t.Helper()
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/register", body: creds(email, password)}), http.StatusAccepted, "")
}

func (a *api) verify(email string) {
	a.t.Helper()
	a.mail.mu.Lock()
	token := a.mail.verify[email]
	a.mail.mu.Unlock()
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/verify-email", body: `{"token":"` + token + `"}`}), http.StatusNoContent, "")
}

// account registers and verifies through the API and adds extra roles.
func (a *api) account(name string, roles ...identity.Role) (string, identity.UserID) {
	a.t.Helper()
	email := a.f.Prefix + "-" + name + "@example.test"
	a.register(email)
	a.verify(email)
	var id identity.UserID
	ctx := context.Background()
	if err := a.f.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		a.t.Fatal(err)
	}
	for _, r := range roles {
		if _, err := a.f.Pool.Exec(ctx, `INSERT INTO user_roles (user_id, role, granted_at, granted_by_kind, granted_by_id)
			VALUES ($1, $2, $3, 'system', 'test')`, id, r, pgtest.Now); err != nil {
			a.t.Fatal(err)
		}
	}
	return email, id
}

type loginBody struct {
	Status        string   `json:"status"`
	Challenge     string   `json:"challenge"`
	MFA           bool     `json:"mfa"`
	RecoveryCodes []string `json:"recovery_codes"`
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "__Host-session" {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", w.Result().Cookies())
	return nil
}

// login signs in through the API, enrolling TOTP or entering a code when
// the server asks. step picks which time step's code to send (0 = now).
func (a *api) login(email string) (*http.Cookie, loginBody) {
	a.t.Helper()
	w := a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(email, password)})
	a.expect(w, http.StatusOK, "")
	body := decode[loginBody](a.t, w)
	switch body.Status {
	case "authenticated":
		return sessionCookie(a.t, w), body
	case "mfa_enrollment_required":
		w := a.do(call{method: "POST", path: "/api/v1/auth/login/mfa/enroll", body: `{"challenge":"` + body.Challenge + `"}`})
		a.expect(w, http.StatusOK, "")
		en := decode[struct {
			Secret string `json:"secret"`
			URI    string `json:"otpauth_uri"`
		}](a.t, w)
		secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
		if err != nil || !strings.HasPrefix(en.URI, "otpauth://totp/") {
			a.t.Fatalf("enrollment = %+v", en)
		}
		a.secrets[email] = secret
	case "mfa_required":
	default:
		a.t.Fatalf("login body = %s", w.Body)
	}
	code := totp.Code(a.secrets[email], totp.Step(pgtest.Now))
	w = a.do(call{method: "POST", path: "/api/v1/auth/login/mfa", body: `{"challenge":"` + body.Challenge + `","code":"` + code + `"}`})
	a.expect(w, http.StatusOK, "")
	return sessionCookie(a.t, w), decode[loginBody](a.t, w)
}

func TestAuthFlow(t *testing.T) {
	a := newAPI(t, options{})
	// The DTO is an allowlist: a client cannot register itself as admin.
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/register",
		body: `{"email":"x@example.test","password":"` + password + `","roles":["admin"]}`}), http.StatusBadRequest, "request.invalid_body")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/register", body: `{"email":"nope","password":"` + password + `"}`}),
		http.StatusBadRequest, "validation_failed")
	w := a.do(call{method: "POST", path: "/api/v1/auth/login", body: `{"email":"a@example.test"}`, header: map[string]string{"Content-Type": "text/plain"}})
	a.expect(w, http.StatusUnsupportedMediaType, "request.unsupported_media_type")

	email := a.f.Prefix + "-alice@example.test"
	a.register(email)
	// Not verified yet: the right password is still the generic 401.
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(email, password)}), http.StatusUnauthorized, "identity.invalid_credentials")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/verify-email/resend", body: `{"email":"` + email + `"}`}), http.StatusAccepted, "")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/verify-email", body: `{"token":"bogus"}`}), http.StatusBadRequest, "identity.invalid_verification_token")
	a.verify(email)
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(email, "wrong password!")}), http.StatusUnauthorized, "identity.invalid_credentials")

	w = a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(email, password)})
	a.expect(w, http.StatusOK, "")
	if w.Header().Get("X-Request-ID") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("missing security headers: %v", w.Header())
	}
	cookie := sessionCookie(t, w)
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge <= 0 {
		t.Fatalf("session cookie = %+v", cookie)
	}

	a.expect(a.do(call{method: "GET", path: "/api/v1/auth/me"}), http.StatusUnauthorized, "identity.unauthenticated")
	w = a.do(call{method: "GET", path: "/api/v1/auth/me", cookie: cookie})
	a.expect(w, http.StatusOK, "")
	me := decode[struct {
		Roles       []identity.Role       `json:"roles"`
		Permissions []identity.Permission `json:"permissions"`
		MFA         bool                  `json:"mfa"`
	}](t, w)
	if !slices.Equal(me.Roles, []identity.Role{identity.RoleCustomer}) || !slices.Contains(me.Permissions, identity.PermBookingCreateOwn) || me.MFA {
		t.Fatalf("me = %s", w.Body)
	}

	w = a.do(call{method: "POST", path: "/api/v1/auth/logout", cookie: cookie})
	a.expect(w, http.StatusNoContent, "")
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("logout must clear the cookie: %+v", c)
	}
	a.expect(a.do(call{method: "GET", path: "/api/v1/auth/me", cookie: cookie}), http.StatusUnauthorized, "identity.unauthenticated")
}

func TestAdminLoginNeedsTOTPAndCanResetAnothersMFA(t *testing.T) {
	a := newAPI(t, options{})
	adminEmail, _ := a.account("admin", identity.RoleAdmin)
	otherEmail, otherID := a.account("admin2", identity.RoleAdmin)

	w := a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(adminEmail, password)})
	if body := decode[loginBody](t, w); body.Status != "mfa_enrollment_required" || body.Challenge == "" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("admin password step must not set a cookie: %s", w.Body)
	}
	admin, body := a.login(adminEmail)
	if !body.MFA || len(body.RecoveryCodes) != totp.RecoveryCodeCount {
		t.Fatalf("enrolling login = %+v", body)
	}
	other, _ := a.login(otherEmail)

	w = a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(adminEmail, password)})
	ch := decode[loginBody](t, w).Challenge
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login/mfa", body: `{"challenge":"` + ch + `","code":"000000"}`}),
		http.StatusBadRequest, "identity.invalid_mfa_code")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login/mfa", body: `{"challenge":"bogus","code":"000000"}`}),
		http.StatusUnauthorized, "identity.invalid_login_challenge")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/mfa/disable", body: `{"code":"000000"}`, cookie: admin}),
		http.StatusForbidden, "identity.mfa_required")

	reset := "/api/v1/admin/users/" + string(otherID) + "/mfa-reset"
	a.expect(a.do(call{method: "POST", path: reset, body: `{"reason":"lost phone"}`, cookie: admin}), http.StatusNoContent, "")
	a.expect(a.do(call{method: "GET", path: "/api/v1/auth/me", cookie: other}), http.StatusUnauthorized, "identity.unauthenticated")
	w = a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(otherEmail, password)})
	if decode[loginBody](t, w).Status != "mfa_enrollment_required" {
		t.Fatalf("after a reset the admin must enrol again: %s", w.Body)
	}
}

func TestCustomerCanOptIntoTOTP(t *testing.T) {
	a := newAPI(t, options{})
	email, _ := a.account("dana")
	cookie, _ := a.login(email)
	w := a.do(call{method: "POST", path: "/api/v1/auth/mfa/setup", cookie: cookie})
	a.expect(w, http.StatusOK, "")
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(decode[struct {
		Secret string `json:"secret"`
	}](t, w).Secret)
	a.secrets[email] = secret
	w = a.do(call{method: "POST", path: "/api/v1/auth/mfa/setup/confirm", cookie: cookie,
		body: `{"code":"` + totp.Code(secret, totp.Step(pgtest.Now)) + `"}`})
	a.expect(w, http.StatusOK, "")
	if codes := decode[struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}](t, w).RecoveryCodes; len(codes) != totp.RecoveryCodeCount {
		t.Fatalf("recovery codes = %v", codes)
	}
	w = a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(email, password)})
	if decode[loginBody](t, w).Status != "mfa_required" {
		t.Fatalf("an enrolled customer must be challenged: %s", w.Body)
	}
}

func TestCrossOriginStateChangesAreRefused(t *testing.T) {
	a := newAPI(t, options{})
	email, _ := a.account("bob")
	body := creds(email, password)
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login", body: body,
		header: map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}}), http.StatusForbidden, "request.cross_origin")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login", body: body,
		header: map[string]string{"Origin": "https://evil.example"}}), http.StatusForbidden, "request.cross_origin")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login", body: body,
		header: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://api.example.test"}}), http.StatusOK, "")
}

func TestCORSAllowsOnlyTheConfiguredFrontend(t *testing.T) {
	front := "https://shop.example.test"
	a := newAPI(t, options{origins: []string{front}})
	preflight := func(origin, headers string) *httptest.ResponseRecorder {
		return a.do(call{method: "OPTIONS", path: "/api/v1/auth/login", header: map[string]string{
			"Origin": origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": headers}})
	}
	w := preflight(front, "content-type")
	a.expect(w, http.StatusNoContent, "")
	if w.Header().Get("Access-Control-Allow-Origin") != front || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("preflight headers = %v", w.Header())
	}
	a.expect(preflight("https://evil.example", "content-type"), http.StatusForbidden, "request.cross_origin")
	a.expect(preflight(front, "content-type, x-admin"), http.StatusForbidden, "request.cross_origin")

	email, _ := a.account("carol")
	w = a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(email, password),
		header: map[string]string{"Origin": front, "Sec-Fetch-Site": "same-site"}})
	a.expect(w, http.StatusOK, "")
	if w.Header().Get("Access-Control-Allow-Origin") != front {
		t.Fatalf("allowed origin must get CORS headers: %v", w.Header())
	}
	w = a.do(call{method: "GET", path: "/healthz", header: map[string]string{"Origin": "https://evil.example"}})
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("other origins must get no CORS headers")
	}
}

func TestAdminEndpointsCheckPermissions(t *testing.T) {
	a := newAPI(t, options{})
	adminEmail, _ := a.account("admin", identity.RoleAdmin)
	staffEmail, _ := a.account("staff", identity.RoleRentalStaff)
	customerEmail, customerID := a.account("carol")
	admin, _ := a.login(adminEmail)
	staff, _ := a.login(staffEmail)
	customer, _ := a.login(customerEmail)
	grant := "/api/v1/admin/users/" + string(customerID) + "/roles/rental_staff"
	reason := `{"reason":"hired"}`

	a.expect(a.do(call{method: "PUT", path: grant, body: reason, cookie: customer}), http.StatusForbidden, "identity.forbidden")
	a.expect(a.do(call{method: "PUT", path: grant, body: reason, cookie: staff}), http.StatusForbidden, "identity.forbidden")
	a.expect(a.do(call{method: "GET", path: "/api/v1/admin/users/" + string(customerID), cookie: staff}), http.StatusForbidden, "identity.forbidden")
	a.expect(a.do(call{method: "GET", path: "/api/v1/admin/permission-matrix", cookie: customer}), http.StatusForbidden, "identity.forbidden")
	a.expect(a.do(call{method: "POST", path: "/api/v1/admin/users/" + string(customerID) + "/mfa-reset", body: reason, cookie: staff}),
		http.StatusForbidden, "identity.forbidden")
	a.expect(a.do(call{method: "PUT", path: grant, body: `{"reason":" "}`, cookie: admin}), http.StatusBadRequest, "validation_failed")
	a.expect(a.do(call{method: "PUT", path: "/api/v1/admin/users/" + string(customerID) + "/roles/superuser", body: reason, cookie: admin}),
		http.StatusBadRequest, "validation_failed")
	a.expect(a.do(call{method: "PUT", path: "/api/v1/admin/users/nobody/roles/finance", body: reason, cookie: admin}),
		http.StatusNotFound, "storage.not_found")

	a.expect(a.do(call{method: "PUT", path: grant, body: reason, cookie: admin}), http.StatusNoContent, "")
	// The grant ended the customer's sessions.
	a.expect(a.do(call{method: "GET", path: "/api/v1/auth/me", cookie: customer}), http.StatusUnauthorized, "identity.unauthenticated")
	w := a.do(call{method: "GET", path: "/api/v1/admin/users/" + string(customerID), cookie: admin})
	a.expect(w, http.StatusOK, "")
	if !strings.Contains(w.Body.String(), `"rental_staff"`) {
		t.Fatalf("user = %s", w.Body)
	}
	a.expect(a.do(call{method: "DELETE", path: grant, body: reason, cookie: admin}), http.StatusNoContent, "")

	lock := "/api/v1/admin/users/" + string(customerID) + "/lock"
	a.expect(a.do(call{method: "POST", path: lock, body: reason, cookie: staff}), http.StatusForbidden, "identity.forbidden")
	a.expect(a.do(call{method: "POST", path: lock, body: `{"reason":"fraud"}`, cookie: admin}), http.StatusNoContent, "")
	a.expect(a.do(call{method: "POST", path: "/api/v1/auth/login", body: creds(customerEmail, password)}),
		http.StatusUnauthorized, "identity.invalid_credentials")
	a.expect(a.do(call{method: "POST", path: "/api/v1/admin/users/" + string(customerID) + "/unlock", body: `{"reason":"cleared"}`, cookie: admin}),
		http.StatusNoContent, "")
	a.login(customerEmail)
}

func TestLoginIsRateLimitedPerEmail(t *testing.T) {
	limits := httpapi.DefaultRateLimits()
	limits.LoginPerEmail = ratelimit.Rate{N: 2, Window: 15 * time.Minute}
	a := newAPI(t, options{limits: limits})
	email, _ := a.account("dave")
	wrong := call{method: "POST", path: "/api/v1/auth/login", body: creds(email, "wrong password!")}
	a.expect(a.do(wrong), http.StatusUnauthorized, "identity.invalid_credentials")
	a.expect(a.do(wrong), http.StatusUnauthorized, "identity.invalid_credentials")
	w := a.do(wrong)
	a.expect(w, http.StatusTooManyRequests, "request.rate_limited")
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 needs Retry-After")
	}
}

// Behind a trusted proxy the client address comes from X-Forwarded-For;
// from anyone else the header is ignored.
func TestClientAddressBehindATrustedProxy(t *testing.T) {
	limits := httpapi.DefaultRateLimits()
	limits.RegisterPerIP = ratelimit.Rate{N: 1, Window: time.Hour}
	a := newAPI(t, options{limits: limits, proxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	reg := func(remote, xff string, n int) *httptest.ResponseRecorder {
		return a.do(call{method: "POST", path: "/api/v1/auth/register", remote: remote,
			body: creds(a.f.Prefix+"-"+string(rune('a'+n))+"@example.test", password), header: map[string]string{"X-Forwarded-For": xff}})
	}
	a.expect(reg("10.0.0.1:1000", "198.51.100.7, 203.0.113.5", 0), http.StatusAccepted, "")
	// Same client (right-most untrusted hop) through another proxy: limited.
	a.expect(reg("10.0.0.2:1000", "203.0.113.5", 1), http.StatusTooManyRequests, "request.rate_limited")
	// A spoofed left-most hop does not change the client.
	a.expect(reg("10.0.0.1:1000", "1.2.3.4, 203.0.113.5", 2), http.StatusTooManyRequests, "request.rate_limited")
	a.expect(reg("10.0.0.1:1000", "203.0.113.6", 3), http.StatusAccepted, "")
	// An untrusted peer cannot pick its address with the header.
	a.expect(reg("192.0.2.9:1000", "203.0.113.7", 4), http.StatusAccepted, "")
	a.expect(reg("192.0.2.9:1000", "203.0.113.8", 5), http.StatusTooManyRequests, "request.rate_limited")
}

func TestHealthEndpoints(t *testing.T) {
	a := newAPI(t, options{})
	a.expect(a.do(call{method: "GET", path: "/healthz"}), http.StatusOK, "")
	a.expect(a.do(call{method: "GET", path: "/readyz"}), http.StatusOK, "")
	a.expect(a.do(call{method: "GET", path: "/nope"}), http.StatusNotFound, "request.not_found")
}
