package identityapp_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/argon2id"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/identity/totp"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/platform/secretbox"
)

const password = "correct horse battery"

type sentMail struct{ email, token string }

// outbox records what would have been emailed.
type outbox struct {
	mu       sync.Mutex
	verifies []sentMail
	resets   []sentMail
	exists   []string
}

func (o *outbox) VerifyEmail(_ context.Context, email, token string, _ time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.verifies = append(o.verifies, sentMail{email, token})
	return nil
}

func (o *outbox) PasswordReset(_ context.Context, email, token string, _ time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.resets = append(o.resets, sentMail{email, token})
	return nil
}

func (o *outbox) AccountExists(_ context.Context, email string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.exists = append(o.exists, email)
	return nil
}

func last(t *testing.T, o *outbox, list *[]sentMail, email string) sentMail {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := len(*list) - 1; i >= 0; i-- {
		if (*list)[i].email == email {
			return (*list)[i]
		}
	}
	t.Fatalf("no email sent to %s", email)
	return sentMail{}
}

type authEnv struct {
	f     *pgtest.Fixture
	clock *pgtest.Clock
	svc   *identityapp.Service
	auth  *identityapp.Auth
	mail  *outbox

	mu      sync.Mutex
	secrets map[string][]byte // TOTP secret per email
	used    map[string]int64  // last TOTP step used per email
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	return newAuthEnvOn(t, pgtest.New(t))
}

func newAuthEnvOn(t *testing.T, f *pgtest.Fixture) *authEnv {
	t.Helper()
	clock := pgtest.NewClock(pgtest.Now)
	svc := identityapp.New(f.Pool, identityapp.WithClock(clock.Now))
	hasher, err := argon2id.New(argon2id.OWASP, 4)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	mail := &outbox{}
	auth, err := identityapp.NewAuth(svc, identityapp.DefaultAuthConfig(), hasher, mail, box)
	if err != nil {
		t.Fatal(err)
	}
	return &authEnv{f: f, clock: clock, svc: svc, auth: auth, mail: mail, secrets: map[string][]byte{}, used: map[string]int64{}}
}

// register signs up and verifies the email.
func (e *authEnv) register(t *testing.T, email string) identity.UserID {
	t.Helper()
	ctx := context.Background()
	if err := e.auth.Register(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	if err := e.auth.VerifyEmail(ctx, last(t, e.mail, &e.mail.verifies, email).token); err != nil {
		t.Fatal(err)
	}
	var id identity.UserID
	if err := e.f.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// account registers a verified user and adds extra roles.
func (e *authEnv) account(t *testing.T, name string, roles ...identity.Role) (string, identity.UserID) {
	t.Helper()
	email := e.f.Prefix + "-" + name + "@example.test"
	id := e.register(t, email)
	for _, r := range roles {
		if _, err := e.f.Pool.Exec(context.Background(), `INSERT INTO user_roles (user_id, role, granted_at, granted_by_kind, granted_by_id)
			VALUES ($1, $2, $3, 'system', 'test')`, id, r, pgtest.Now); err != nil {
			t.Fatal(err)
		}
	}
	return email, id
}

// code returns an unused TOTP code for email's secret.
func (e *authEnv) code(t *testing.T, email string) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := totp.Step(e.clock.Now())
	step := max(cur-1, e.used[email]+1)
	if step > cur+1 {
		t.Fatalf("test used every TOTP step around now for %s", email)
	}
	e.used[email] = step
	return totp.Code(e.secrets[email], step)
}

func (e *authEnv) remember(t *testing.T, email string, en identityapp.Enrollment) {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	if err != nil || !strings.Contains(en.URI, "secret="+en.Secret) {
		t.Fatalf("enrollment = %+v, %v", en, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.secrets[email], e.used[email] = secret, 0
}

// login signs in, passing the second factor (enrolling first) when asked.
func (e *authEnv) login(t *testing.T, email, pw string) identityapp.Login {
	t.Helper()
	ctx := context.Background()
	res, err := e.auth.Login(ctx, email, pw, "")
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	if res.Session != nil {
		return *res.Session
	}
	if res.Challenge.Enroll {
		en, err := e.auth.BeginLoginEnrollment(ctx, res.Challenge.Token)
		if err != nil {
			t.Fatal(err)
		}
		e.remember(t, email, en)
	}
	l, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, e.code(t, email), "")
	if err != nil {
		t.Fatalf("second factor for %s: %v", email, err)
	}
	return l
}

func (e *authEnv) principal(t *testing.T, token string) identity.Principal {
	t.Helper()
	p, _, err := e.auth.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

func (e *authEnv) dead(t *testing.T, token, why string) {
	t.Helper()
	if _, _, err := e.auth.Authenticate(context.Background(), token); !errors.Is(err, identity.ErrNotPrincipal) {
		t.Fatalf("%s: authenticate err = %v", why, err)
	}
}

func (e *authEnv) audited(t *testing.T, action string, user identity.UserID, result string) {
	t.Helper()
	var n int
	if err := e.f.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events
		WHERE action = $1 AND resource_id = $2 AND result = $3`, action, user, result).Scan(&n); err != nil || n == 0 {
		t.Fatalf("missing audit %s %s for %s (%v)", result, action, user, err)
	}
}

func TestRegisterVerifyLoginLogout(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email := e.f.Prefix + "-alice@example.test"
	if err := e.auth.Register(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	first := last(t, e.mail, &e.mail.verifies, email)
	var id identity.UserID
	if err := e.f.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.audited(t, "user.register", id, "SUCCESS")

	// Unverified: even the right password is the generic failure.
	if _, err := e.auth.Login(ctx, email, password, ""); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("unverified login err = %v", err)
	}
	e.audited(t, "auth.login", id, "DENIED")
	// Registering again while unverified sends a fresh link instead.
	if err := e.auth.Register(ctx, strings.ToUpper(email), "another long password"); err != nil {
		t.Fatal(err)
	}
	second := last(t, e.mail, &e.mail.verifies, email)
	if second.token == first.token || len(e.mail.exists) != 0 {
		t.Fatalf("re-registration of an unverified email must resend verification: %v", e.mail.exists)
	}
	if err := e.auth.VerifyEmail(ctx, "garbage"); !errors.Is(err, identity.ErrInvalidVerifyToken) {
		t.Fatalf("malformed token err = %v", err)
	}
	if err := e.auth.VerifyEmail(ctx, second.token); err != nil {
		t.Fatal(err)
	}
	e.audited(t, "user.email_verify", id, "SUCCESS")
	if err := e.auth.VerifyEmail(ctx, first.token); !errors.Is(err, identity.ErrInvalidVerifyToken) {
		t.Fatalf("an older link must die once one is used: %v", err)
	}
	// Verified: re-registration only notifies the owner.
	if err := e.auth.Register(ctx, email, "another long password"); err != nil {
		t.Fatal(err)
	}
	if len(e.mail.exists) != 1 || e.mail.exists[0] != email {
		t.Fatalf("account-exists notices = %v", e.mail.exists)
	}
	if err := e.auth.ResendVerification(ctx, email); err != nil || len(e.mail.verifies) != 2 {
		t.Fatalf("resend for a verified email must send nothing: %v, %d", err, len(e.mail.verifies))
	}
	if err := e.auth.Register(ctx, "x@example.test", "short"); !errors.Is(err, domainerr.ErrValidation) {
		t.Fatalf("weak password err = %v", err)
	}

	for name, try := range map[string][2]string{
		"wrong password": {email, "wrong password!!"},
		"unknown email":  {e.f.Prefix + "-nobody@example.test", password},
		"bad email":      {"not an email", password},
	} {
		if _, err := e.auth.Login(ctx, try[0], try[1], ""); !errors.Is(err, identity.ErrInvalidCredentials) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	l := e.login(t, strings.ToUpper(email), password)
	if len(l.Token) < 40 || !l.Session.ExpiresAt.Equal(pgtest.Now.Add(7*24*time.Hour)) || l.Session.MFAAt != nil {
		t.Fatalf("customer session = %+v", l.Session)
	}
	p := e.principal(t, l.Token)
	if p.UserID != id || !p.Can(identity.PermBookingCreateOwn) || p.Can(identity.PermBookingReadAll) {
		t.Fatalf("principal = %+v", p)
	}
	var stored []byte
	if err := e.f.Pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE id = $1`, l.Session.ID).Scan(&stored); err != nil ||
		strings.Contains(string(stored), l.Token) {
		t.Fatal("only a hash of the token may be stored")
	}
	e.audited(t, "auth.login", id, "SUCCESS")

	if err := e.auth.Logout(ctx, l.Token); err != nil {
		t.Fatal(err)
	}
	e.dead(t, l.Token, "after logout")
	if err := e.auth.Logout(ctx, l.Token); err != nil {
		t.Fatalf("logout must be idempotent: %v", err)
	}
	e.dead(t, "garbage", "malformed token")
}

func TestVerificationLinksExpire(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email := e.f.Prefix + "-late@example.test"
	if err := e.auth.Register(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	link := last(t, e.mail, &e.mail.verifies, email)
	e.clock.Set(pgtest.Now.Add(24 * time.Hour))
	if err := e.auth.VerifyEmail(ctx, link.token); !errors.Is(err, identity.ErrInvalidVerifyToken) {
		t.Fatalf("expired link err = %v", err)
	}
	if err := e.auth.ResendVerification(ctx, email); err != nil {
		t.Fatal(err)
	}
	if err := e.auth.VerifyEmail(ctx, last(t, e.mail, &e.mail.verifies, email).token); err != nil {
		t.Fatal(err)
	}
	e.login(t, email, password)
}

func TestLoginReplacesThePresentedSession(t *testing.T) {
	e := newAuthEnv(t)
	email, _ := e.account(t, "bob")
	first := e.login(t, email, password)
	second, err := e.auth.Login(context.Background(), email, password, first.Token)
	if err != nil || second.Session == nil {
		t.Fatal(err)
	}
	if second.Session.Token == first.Token || second.Session.Session.ID == first.Session.ID {
		t.Fatal("login must issue a new session")
	}
	e.dead(t, first.Token, "the session presented at login")
	e.principal(t, second.Session.Token)
}

func TestSessionIdleAndAbsoluteLimits(t *testing.T) {
	e := newAuthEnv(t)
	staffEmail, _ := e.account(t, "staff", identity.RoleRentalStaff)
	staff := e.login(t, staffEmail, password)
	if !staff.Session.ExpiresAt.Equal(pgtest.Now.Add(12 * time.Hour)) {
		t.Fatalf("staff session expires %v", staff.Session.ExpiresAt)
	}
	// Activity slides the 30-minute idle window.
	e.clock.Set(pgtest.Now.Add(29 * time.Minute))
	e.principal(t, staff.Token)
	e.clock.Set(pgtest.Now.Add(58 * time.Minute))
	e.principal(t, staff.Token)
	e.clock.Set(pgtest.Now.Add(89 * time.Minute))
	e.dead(t, staff.Token, "staff idle for 31 minutes")

	e.clock.Set(pgtest.Now)
	customerEmail, _ := e.account(t, "customer")
	customer := e.login(t, customerEmail, password)
	for h := 20; h < 7*24; h += 20 {
		e.clock.Set(pgtest.Now.Add(time.Duration(h) * time.Hour))
		e.principal(t, customer.Token)
	}
	e.clock.Set(pgtest.Now.Add(7 * 24 * time.Hour))
	e.dead(t, customer.Token, "customer session past its lifetime")
}

func TestRoleChangesAndLocksRevokeSessions(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	adminEmail, _ := e.account(t, "admin", identity.RoleAdmin)
	staffEmail, _ := e.account(t, "staff", identity.RoleRentalStaff)
	email, id := e.account(t, "carol")
	admin := e.principal(t, e.login(t, adminEmail, password).Token)
	staff := e.principal(t, e.login(t, staffEmail, password).Token)
	session := e.login(t, email, password)

	if err := e.svc.GrantRole(ctx, staff, id, identity.RoleFinance, "x"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff granting err = %v", err)
	}
	if err := e.svc.GrantRole(ctx, admin, admin.UserID, identity.RoleFinance, "x"); !errors.Is(err, identity.ErrSelfRoleChange) {
		t.Fatalf("self grant err = %v", err)
	}
	if err := e.svc.GrantRole(ctx, admin, id, identity.RoleRentalStaff, "hired"); err != nil {
		t.Fatal(err)
	}
	e.dead(t, session.Token, "after a role grant")
	var to string
	if err := e.f.Pool.QueryRow(ctx, `SELECT changes->'roles'->>'To' FROM audit_events
		WHERE action = 'user.role_grant' AND resource_id = $1`, id).Scan(&to); err != nil || to != "customer,rental_staff" {
		t.Fatalf("role grant audit = %q, %v", to, err)
	}
	relogin := e.login(t, email, password)
	if !relogin.Principal.Can(identity.PermRentalHandover) || !relogin.Session.ExpiresAt.Equal(pgtest.Now.Add(12*time.Hour)) {
		t.Fatal("new roles and staff session limits must apply at the next login")
	}

	if err := e.svc.LockUser(ctx, admin, id, "left the company"); err != nil {
		t.Fatal(err)
	}
	e.dead(t, relogin.Token, "after a lock")
	if _, err := e.auth.Login(ctx, email, password, ""); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("locked login err = %v", err)
	}
	e.audited(t, "auth.login", id, "DENIED")
	if err := e.svc.UnlockUser(ctx, admin, id, "rehired"); err != nil {
		t.Fatal(err)
	}
	e.login(t, email, password)

	// Finance requires a second factor: the next login must enrol.
	if err := e.svc.GrantRole(ctx, admin, id, identity.RoleFinance, "moved to accounting"); err != nil {
		t.Fatal(err)
	}
	res, err := e.auth.Login(ctx, email, password, "")
	if err != nil || res.Challenge == nil || !res.Challenge.Enroll {
		t.Fatalf("finance login must require enrolment: %+v, %v", res, err)
	}

	u, err := e.svc.User(ctx, admin, id)
	if err != nil || u.Email != email || u.Status != identity.UserActive || u.EmailVerifiedAt == nil {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if _, err := e.svc.User(ctx, staff, id); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff reading accounts err = %v", err)
	}
}

// Two admins demoting each other at once must not leave the system without
// an administrator: the second change sees its actor is no longer admin.
func TestConcurrentMutualDemotionKeepsAnAdmin(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	aEmail, aID := e.account(t, "admin-a", identity.RoleAdmin)
	bEmail, bID := e.account(t, "admin-b", identity.RoleAdmin)
	a := e.principal(t, e.login(t, aEmail, password).Token)
	b := e.principal(t, e.login(t, bEmail, password).Token)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = e.svc.RevokeRole(ctx, a, bID, identity.RoleAdmin, "x") }()
	go func() { defer wg.Done(); errs[1] = e.svc.RevokeRole(ctx, b, aID, identity.RoleAdmin, "x") }()
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("exactly one demotion must succeed: %v", errs)
	}
	for _, err := range errs {
		if err != nil && !errors.Is(err, identity.ErrForbidden) {
			t.Fatalf("losing demotion err = %v", err)
		}
	}
	var admins int
	if err := e.f.Pool.QueryRow(ctx, `SELECT count(*) FROM user_roles WHERE role = 'admin' AND user_id IN ($1, $2)`, aID, bID).Scan(&admins); err != nil || admins != 1 {
		t.Fatalf("admins left = %d, %v", admins, err)
	}
}

func TestMandatoryMFAForAdmins(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email, id := e.account(t, "owner", identity.RoleAdmin)

	res, err := e.auth.Login(ctx, email, password, "")
	if err != nil || res.Session != nil || res.Challenge == nil || !res.Challenge.Enroll {
		t.Fatalf("admin login must stop at an enrolment challenge: %+v, %v", res, err)
	}
	ch := res.Challenge.Token
	if _, err := e.auth.CompleteLogin(ctx, ch, "123456", ""); !errors.Is(err, identity.ErrMFANotEnabled) {
		t.Fatalf("completing before enrolment err = %v", err)
	}
	en, err := e.auth.BeginLoginEnrollment(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	e.remember(t, email, en)
	var stored []byte
	if err := e.f.Pool.QueryRow(ctx, `SELECT secret_ciphertext FROM user_mfa WHERE user_id = $1`, id).Scan(&stored); err != nil ||
		strings.Contains(string(stored), string(e.secrets[email])) {
		t.Fatal("the TOTP secret must be stored encrypted")
	}
	if _, err := e.auth.CompleteLogin(ctx, ch, "000000", ""); !errors.Is(err, identity.ErrInvalidMFACode) {
		t.Fatalf("wrong code err = %v", err)
	}
	e.audited(t, "auth.mfa", id, "DENIED")
	l, err := e.auth.CompleteLogin(ctx, ch, e.code(t, email), "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Session.MFAAt == nil || len(l.RecoveryCodes) != totp.RecoveryCodeCount {
		t.Fatalf("enrolling login = %+v", l)
	}
	e.audited(t, "user.mfa_enable", id, "SUCCESS")
	if p := e.principal(t, l.Token); !p.Can(identity.PermUserRoleManage) {
		t.Fatal("admin session must work")
	}
	if _, err := e.auth.CompleteLogin(ctx, ch, "123456", ""); !errors.Is(err, identity.ErrInvalidChallenge) {
		t.Fatalf("a used challenge err = %v", err)
	}

	// Later logins ask for a code; enrolment cannot be restarted.
	res, _ = e.auth.Login(ctx, email, password, "")
	if res.Challenge == nil || res.Challenge.Enroll {
		t.Fatalf("second login = %+v", res)
	}
	if _, err := e.auth.BeginLoginEnrollment(ctx, res.Challenge.Token); !errors.Is(err, identity.ErrMFAAlreadyEnabled) {
		t.Fatalf("re-enrolment err = %v", err)
	}
	// A code already accepted cannot be replayed.
	replay := totp.Code(e.secrets[email], e.used[email])
	if _, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, replay, ""); !errors.Is(err, identity.ErrInvalidMFACode) {
		t.Fatalf("replayed code err = %v", err)
	}
	// A recovery code works once, typed loosely.
	rc := strings.ToLower(l.RecoveryCodes[0])
	if _, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, rc, ""); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	res, _ = e.auth.Login(ctx, email, password, "")
	if _, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, rc, ""); !errors.Is(err, identity.ErrInvalidMFACode) {
		t.Fatalf("reused recovery code err = %v", err)
	}

	// Five wrong codes end the challenge, even for a right one after.
	res, _ = e.auth.Login(ctx, email, password, "")
	for i := range 5 {
		if _, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, "000000", ""); !errors.Is(err, identity.ErrInvalidMFACode) {
			t.Fatalf("wrong code %d err = %v", i+1, err)
		}
	}
	if _, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, e.code(t, email), ""); !errors.Is(err, identity.ErrInvalidChallenge) {
		t.Fatalf("challenge after 5 wrong codes err = %v", err)
	}
	// Challenges expire.
	res, _ = e.auth.Login(ctx, email, password, "")
	e.clock.Set(pgtest.Now.Add(5 * time.Minute))
	if _, err := e.auth.CompleteLogin(ctx, res.Challenge.Token, e.code(t, email), ""); !errors.Is(err, identity.ErrInvalidChallenge) {
		t.Fatalf("expired challenge err = %v", err)
	}
	if err := e.auth.DisableMFA(ctx, e.principal(t, l.Token), e.code(t, email)); !errors.Is(err, identity.ErrMFARequired) {
		t.Fatalf("admin disabling MFA err = %v", err)
	}
}

// An admin who lost both the device and the recovery codes is reset by
// another admin and enrols again.
func TestAdminResetsAnotherAdminsMFA(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	aEmail, _ := e.account(t, "admin-a", identity.RoleAdmin)
	bEmail, bID := e.account(t, "admin-b", identity.RoleAdmin)
	a := e.principal(t, e.login(t, aEmail, password).Token)
	bSession := e.login(t, bEmail, password)
	staffEmail, _ := e.account(t, "staff", identity.RoleRentalStaff)
	staff := e.principal(t, e.login(t, staffEmail, password).Token)

	if err := e.svc.ResetMFA(ctx, staff, bID, "lost phone"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff reset err = %v", err)
	}
	if err := e.svc.ResetMFA(ctx, a, a.UserID, "lost phone"); !errors.Is(err, identity.ErrSelfMFAReset) {
		t.Fatalf("self reset err = %v", err)
	}
	if err := e.svc.ResetMFA(ctx, a, bID, "lost phone, identity checked in person"); err != nil {
		t.Fatal(err)
	}
	e.dead(t, bSession.Token, "after an MFA reset")
	e.audited(t, "user.mfa_reset", bID, "SUCCESS")
	res, err := e.auth.Login(ctx, bEmail, password, "")
	if err != nil || res.Challenge == nil || !res.Challenge.Enroll {
		t.Fatalf("login after reset must enrol again: %+v, %v", res, err)
	}
	if err := e.svc.ResetMFA(ctx, a, bID, "again"); err != nil {
		t.Fatalf("resetting an absent enrolment must be a no-op: %v", err)
	}
}

func TestOptionalMFAForCustomers(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email, _ := e.account(t, "dana")
	here := e.login(t, email, password)
	elsewhere := e.login(t, email, password)
	p := e.principal(t, here.Token)

	if _, err := e.auth.ConfirmMFASetup(ctx, p, here.Session.ID, "123456"); !errors.Is(err, identity.ErrMFANotEnabled) {
		t.Fatalf("confirm before setup err = %v", err)
	}
	en, err := e.auth.StartMFASetup(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	e.remember(t, email, en)
	if _, err := e.auth.ConfirmMFASetup(ctx, p, here.Session.ID, "000000"); !errors.Is(err, identity.ErrInvalidMFACode) {
		t.Fatalf("wrong code err = %v", err)
	}
	codes, err := e.auth.ConfirmMFASetup(ctx, p, here.Session.ID, e.code(t, email))
	if err != nil || len(codes) != totp.RecoveryCodeCount {
		t.Fatalf("confirm = %v, %v", codes, err)
	}
	if _, s, err := e.auth.Authenticate(ctx, here.Token); err != nil || s.MFAAt == nil {
		t.Fatalf("the confirming session must count as MFA: %+v, %v", s, err)
	}
	e.dead(t, elsewhere.Token, "other session after enabling MFA")
	if _, err := e.auth.StartMFASetup(ctx, p); !errors.Is(err, identity.ErrMFAAlreadyEnabled) {
		t.Fatalf("second setup err = %v", err)
	}
	res, _ := e.auth.Login(ctx, email, password, "")
	if res.Challenge == nil || res.Challenge.Enroll {
		t.Fatalf("a customer with MFA must be challenged: %+v", res)
	}

	if err := e.auth.DisableMFA(ctx, p, "000000"); !errors.Is(err, identity.ErrInvalidMFACode) {
		t.Fatalf("disable with a wrong code err = %v", err)
	}
	if err := e.auth.DisableMFA(ctx, p, e.code(t, email)); err != nil {
		t.Fatal(err)
	}
	if res, err := e.auth.Login(ctx, email, password, ""); err != nil || res.Session == nil {
		t.Fatalf("after disabling, login needs only the password: %+v, %v", res, err)
	}
}

func TestPasswordReset(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email, id := e.account(t, "dave")
	session := e.login(t, email, password)

	if err := e.auth.RequestPasswordReset(ctx, e.f.Prefix+"-nobody@example.test"); err != nil || len(e.mail.resets) != 0 {
		t.Fatalf("unknown email must be accepted silently: %v, %v", err, e.mail.resets)
	}
	if err := e.auth.RequestPasswordReset(ctx, email); err != nil {
		t.Fatal(err)
	}
	older := last(t, e.mail, &e.mail.resets, email)
	if err := e.auth.RequestPasswordReset(ctx, email); err != nil {
		t.Fatal(err)
	}
	link := last(t, e.mail, &e.mail.resets, email)
	if link.token == older.token {
		t.Fatalf("reset mail = %+v", link)
	}

	if err := e.auth.ResetPassword(ctx, strings.Repeat("A", len(link.token)), "brand new password"); !errors.Is(err, identity.ErrInvalidResetToken) {
		t.Fatalf("unknown token err = %v", err)
	}
	if err := e.auth.ResetPassword(ctx, link.token, "short"); !errors.Is(err, domainerr.ErrValidation) {
		t.Fatalf("weak new password err = %v", err)
	}
	if err := e.auth.ResetPassword(ctx, link.token, "brand new password"); err != nil {
		t.Fatal(err)
	}
	e.audited(t, "user.password_reset", id, "SUCCESS")
	e.dead(t, session.Token, "after a password reset")
	if _, err := e.auth.Login(ctx, email, password, ""); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("old password err = %v", err)
	}
	e.login(t, email, "brand new password")
	if err := e.auth.ResetPassword(ctx, link.token, "yet another password"); !errors.Is(err, identity.ErrInvalidResetToken) {
		t.Fatalf("reused token err = %v", err)
	}
	if err := e.auth.ResetPassword(ctx, older.token, "yet another password"); !errors.Is(err, identity.ErrInvalidResetToken) {
		t.Fatalf("older outstanding token err = %v", err)
	}

	if err := e.auth.RequestPasswordReset(ctx, email); err != nil {
		t.Fatal(err)
	}
	expired := last(t, e.mail, &e.mail.resets, email)
	e.clock.Set(pgtest.Now.Add(30 * time.Minute))
	if err := e.auth.ResetPassword(ctx, expired.token, "yet another password"); !errors.Is(err, identity.ErrInvalidResetToken) {
		t.Fatalf("expired token err = %v", err)
	}
}

// The reset link proves control of the mailbox, so it also verifies an
// account whose verification email was lost.
func TestPasswordResetVerifiesTheEmail(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email := e.f.Prefix + "-erin@example.test"
	if err := e.auth.Register(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	if err := e.auth.RequestPasswordReset(ctx, email); err != nil {
		t.Fatal(err)
	}
	if err := e.auth.ResetPassword(ctx, last(t, e.mail, &e.mail.resets, email).token, "brand new password"); err != nil {
		t.Fatal(err)
	}
	e.login(t, email, "brand new password")
}

func TestChangePasswordKeepsOnlyTheCurrentSession(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	email, _ := e.account(t, "erin")
	here := e.login(t, email, password)
	elsewhere := e.login(t, email, password)

	if err := e.auth.ChangePassword(ctx, here.Principal, here.Session.ID, "not my password", "a fresh password"); !errors.Is(err, domainerr.ErrValidation) {
		t.Fatalf("wrong current password err = %v", err)
	}
	if err := e.auth.ChangePassword(ctx, here.Principal, here.Session.ID, password, "a fresh password"); err != nil {
		t.Fatal(err)
	}
	e.principal(t, here.Token)
	e.dead(t, elsewhere.Token, "other session after a password change")
	e.login(t, email, "a fresh password")
}

func TestBootstrapAdminOnlyWhileNoAdminExists(t *testing.T) {
	ctx := context.Background()
	// A database of its own: the package database already has admins.
	url := pgtest.NewDatabase(t)
	if err := postgres.Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Open(ctx, postgres.Config{DSN: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e := newAuthEnvOn(t, &pgtest.Fixture{Pool: pool, Prefix: "boot"})

	id, err := e.auth.BootstrapAdmin(ctx, "owner@example.test", password)
	if err != nil {
		t.Fatal(err)
	}
	// Verified by construction; TOTP is enrolled at the first login.
	l := e.login(t, "owner@example.test", password)
	if l.Principal.UserID != id || !l.Principal.Can(identity.PermUserRoleManage) || len(l.RecoveryCodes) == 0 {
		t.Fatalf("bootstrapped login = %+v", l)
	}
	if _, err := e.auth.BootstrapAdmin(ctx, "second@example.test", password); !errors.Is(err, identity.ErrAdminExists) {
		t.Fatalf("second bootstrap err = %v", err)
	}
}
