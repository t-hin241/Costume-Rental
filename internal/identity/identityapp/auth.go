package identityapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/secretbox"
)

// PasswordHasher is the password hashing adapter (argon2id.Hasher).
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	// Verify reports a match and whether the hash should be upgraded.
	Verify(ctx context.Context, password, encoded string) (ok, rehash bool, err error)
}

// Notifier delivers account emails. It is called after the transaction
// commits and must not block on delivery (notifications.Queue sends in the
// background), so neither the response nor its timing reveals whether an
// account exists; a failure is logged and never changes the response.
type Notifier interface {
	VerifyEmail(ctx context.Context, email, token string, expiresAt time.Time) error
	PasswordReset(ctx context.Context, email, token string, expiresAt time.Time) error
	// AccountExists tells the owner of a verified email that someone tried
	// to register it again.
	AccountExists(ctx context.Context, email string) error
}

// AuthConfig holds the authentication settings (decisions D23 and D24).
type AuthConfig struct {
	Credentials    identity.CredentialPolicy
	Sessions       identity.SessionPolicy
	ResetTokenTTL  time.Duration
	VerifyTokenTTL time.Duration
	// ChallengeTTL bounds the time between the password and the second
	// factor of a login.
	ChallengeTTL time.Duration
	// MFAIssuer names the service in authenticator apps.
	MFAIssuer string
}

// DefaultAuthConfig returns the approved defaults (D23): 12-character
// passwords, customer sessions of 7 days (24 h idle), staff sessions of
// 12 h (30 min idle), 30-minute reset links; and (D24) 24-hour
// verification links and 5-minute login challenges.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		Credentials: identity.CredentialPolicy{MinPasswordLength: 12},
		Sessions: identity.SessionPolicy{
			Customer: identity.SessionLimits{TTL: 7 * 24 * time.Hour, Idle: 24 * time.Hour},
			Staff:    identity.SessionLimits{TTL: 12 * time.Hour, Idle: 30 * time.Minute},
		},
		ResetTokenTTL:  30 * time.Minute,
		VerifyTokenTTL: 24 * time.Hour,
		ChallengeTTL:   5 * time.Minute,
		MFAIssuer:      "Webee Rentor",
	}
}

// Validate checks the guardrails.
func (c AuthConfig) Validate() error {
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	if err := c.Sessions.Validate(); err != nil {
		return err
	}
	for name, ttl := range map[string]time.Duration{"reset token": c.ResetTokenTTL, "verification token": c.VerifyTokenTTL} {
		if ttl <= 0 || ttl > 7*24*time.Hour {
			return fmt.Errorf("%s TTL %v outside (0, 168h]: %w", name, ttl, identity.ErrInvalidAuthConfig)
		}
	}
	if c.ResetTokenTTL > 24*time.Hour {
		return fmt.Errorf("reset token TTL %v above 24h: %w", c.ResetTokenTTL, identity.ErrInvalidAuthConfig)
	}
	if c.ChallengeTTL <= 0 || c.ChallengeTTL > 15*time.Minute {
		return fmt.Errorf("login challenge TTL %v outside (0, 15m]: %w", c.ChallengeTTL, identity.ErrInvalidAuthConfig)
	}
	if strings.TrimSpace(c.MFAIssuer) == "" || strings.Contains(c.MFAIssuer, ":") {
		return fmt.Errorf("MFA issuer %q must be non-empty without ':': %w", c.MFAIssuer, identity.ErrInvalidAuthConfig)
	}
	return nil
}

const (
	// touchInterval limits last_seen_at writes to one per session per minute.
	touchInterval = time.Minute
	// maxChallengeAttempts wrong second-factor codes end a login challenge.
	maxChallengeAttempts = 5
)

// Auth runs the authentication use cases: registration and email
// verification, login (with a second factor when required), session
// checks, logout, password changes and TOTP enrolment.
type Auth struct {
	svc     *Service
	cfg     AuthConfig
	hasher  PasswordHasher
	notify  Notifier
	secrets *secretbox.Box

	dummyOnce sync.Once
	dummyHash string
}

// NewAuth builds the authentication service. secrets encrypts TOTP seeds;
// without it (tools such as bootstrap-admin) TOTP operations fail with
// identity.ErrMFAUnavailable.
func NewAuth(svc *Service, cfg AuthConfig, hasher PasswordHasher, notify Notifier, secrets *secretbox.Box) (*Auth, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if svc == nil || hasher == nil || notify == nil {
		return nil, errors.New("identityapp: NewAuth needs a service, a hasher and a notifier")
	}
	return &Auth{svc: svc, cfg: cfg, hasher: hasher, notify: notify, secrets: secrets}, nil
}

// Config returns the active settings, e.g. for the session cookie lifetime.
func (a *Auth) Config() AuthConfig { return a.cfg }

// Register creates an unverified customer account and emails a
// verification link. The result is the same whether or not the email is
// taken: an unverified owner gets a fresh link, a verified one a notice.
// The account cannot log in until verified, so registering someone else's
// email reveals nothing either.
func (a *Auth) Register(ctx context.Context, email, password string) error {
	normalized, err := identity.NormalizeEmail(email)
	if err != nil {
		return domainerr.Invalid("email", "must be a valid email address")
	}
	if err := a.cfg.Credentials.CheckPassword("password", password); err != nil {
		return err
	}
	// Hash before looking at the database so both outcomes take as long.
	hash, err := a.hasher.Hash(ctx, password)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	u, err := identity.NewCustomer(identity.UserID(a.svc.newID()), normalized, a.svc.now())
	if err != nil {
		return err
	}
	token, tokenHash := newToken()
	expiresAt := u.CreatedAt.Add(a.cfg.VerifyTokenTTL)
	err = postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		if err := identitypg.InsertUser(ctx, tx, u, hash, identity.SystemActor("registration")); err != nil {
			return err
		}
		if err := identitypg.InsertToken(ctx, tx, identitypg.TokenEmailVerify, tokenHash, u.ID, u.CreatedAt, expiresAt); err != nil {
			return err
		}
		return a.audit(ctx, tx, identity.UserActor(u.ID), "user.register", "user", string(u.ID), audit.ResultSuccess, "", nil)
	})
	if errors.Is(err, postgres.ErrDuplicate) {
		return a.registerTaken(ctx, normalized)
	}
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	a.send(ctx, "verify-email", u.ID, func() error { return a.notify.VerifyEmail(ctx, normalized, token, expiresAt) })
	return nil
}

func (a *Auth) registerTaken(ctx context.Context, email string) error {
	creds, err := identitypg.CredentialsByEmail(ctx, a.svc.pool, email)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if !creds.EmailVerified && creds.Status == identity.UserActive {
		return a.issueVerification(ctx, creds)
	}
	a.send(ctx, "account-exists", creds.UserID, func() error { return a.notify.AccountExists(ctx, email) })
	return nil
}

// ResendVerification emails a new verification link when email belongs to
// an active, unverified account, and otherwise does nothing; the caller
// always gets the same answer.
func (a *Auth) ResendVerification(ctx context.Context, email string) error {
	normalized, err := identity.NormalizeEmail(email)
	if err != nil {
		return domainerr.Invalid("email", "must be a valid email address")
	}
	creds, err := identitypg.CredentialsByEmail(ctx, a.svc.pool, normalized)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && (creds.EmailVerified || creds.Status != identity.UserActive)) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resend verification: %w", err)
	}
	return a.issueVerification(ctx, creds)
}

func (a *Auth) issueVerification(ctx context.Context, creds identitypg.Credentials) error {
	token, tokenHash := newToken()
	now := a.svc.now().UTC()
	expiresAt := now.Add(a.cfg.VerifyTokenTTL)
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		return identitypg.InsertToken(ctx, tx, identitypg.TokenEmailVerify, tokenHash, creds.UserID, now, expiresAt)
	})
	if err != nil {
		return fmt.Errorf("issue verification for %s: %w", creds.UserID, err)
	}
	a.send(ctx, "verify-email", creds.UserID, func() error { return a.notify.VerifyEmail(ctx, creds.Email, token, expiresAt) })
	return nil
}

// VerifyEmail confirms an email with a verification token. The token works
// once; using it invalidates the user's other verification links.
func (a *Auth) VerifyEmail(ctx context.Context, token string) error {
	tokenHash, ok := parseToken(token)
	if !ok {
		return identity.ErrInvalidVerifyToken
	}
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		t, err := identitypg.TokenForUpdate(ctx, tx, identitypg.TokenEmailVerify, tokenHash)
		if errors.Is(err, postgres.ErrNotFound) {
			return identity.ErrInvalidVerifyToken
		}
		if err != nil {
			return err
		}
		now := a.svc.now().UTC()
		if t.Used || !now.Before(t.ExpiresAt) {
			return identity.ErrInvalidVerifyToken
		}
		if err := identitypg.MarkEmailVerified(ctx, tx, t.UserID, now); err != nil {
			return err
		}
		if err := identitypg.ConsumeTokens(ctx, tx, identitypg.TokenEmailVerify, t.UserID, now); err != nil {
			return err
		}
		return a.audit(ctx, tx, identity.UserActor(t.UserID), "user.email_verify", "user", string(t.UserID), audit.ResultSuccess, "", nil)
	})
	if err != nil {
		return fmt.Errorf("verify email: %w", err)
	}
	return nil
}

// Login is a new session. Token is the bearer secret for the client; it is
// returned only here and only its hash is stored. RecoveryCodes is set
// only by the login that completed a TOTP enrolment, and is shown once.
type Login struct {
	Token         string
	Session       identity.Session
	Principal     identity.Principal
	RecoveryCodes []string
}

// Challenge is the second step a login needs before it gets a session:
// enter a TOTP or recovery code, or (Enroll) first set up TOTP because the
// account's roles require it.
type Challenge struct {
	Token     string
	ExpiresAt time.Time
	Enroll    bool
}

// LoginResult holds either a session or a challenge.
type LoginResult struct {
	Session   *Login
	Challenge *Challenge
}

// Login checks email and password. Accounts with a second factor (enabled,
// or required by their roles) get a Challenge for CompleteLogin; others get
// a session at once. Any session the client presented (previousToken) is
// revoked when a session is issued, so the session ID always changes at
// login. Every failure, including an unverified email or a locked account,
// is identity.ErrInvalidCredentials.
func (a *Auth) Login(ctx context.Context, email, password, previousToken string) (LoginResult, error) {
	normalized, err := identity.NormalizeEmail(email)
	if err != nil || utf8.RuneCountInString(password) > identity.MaxPasswordLength {
		a.dummyVerify(ctx, password)
		return LoginResult{}, identity.ErrInvalidCredentials
	}
	creds, err := identitypg.CredentialsByEmail(ctx, a.svc.pool, normalized)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && creds.PasswordHash == "") {
		// Spend the same time as a real check so timing does not reveal
		// which emails have accounts.
		a.dummyVerify(ctx, password)
		return LoginResult{}, identity.ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	ok, rehash, err := a.hasher.Verify(ctx, password, creds.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login %s: %w", creds.UserID, err)
	}
	if reason := denyReason(ok, creds); reason != "" {
		if err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
			return a.audit(ctx, tx, identity.SystemActor("login"), "auth.login", "user", string(creds.UserID), audit.ResultDenied, reason, nil)
		}); err != nil {
			return LoginResult{}, fmt.Errorf("login %s: %w", creds.UserID, err)
		}
		return LoginResult{}, identity.ErrInvalidCredentials
	}
	var newHash string
	if rehash {
		if newHash, err = a.hasher.Hash(ctx, password); err != nil {
			return LoginResult{}, fmt.Errorf("login %s: rehash: %w", creds.UserID, err)
		}
	}

	var out LoginResult
	err = postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		// Re-checks the account status inside the transaction.
		p, err := a.loadPrincipal(ctx, tx, creds.UserID)
		if err != nil {
			return err
		}
		now := a.svc.now().UTC()
		if newHash != "" {
			if err := identitypg.SetPasswordHash(ctx, tx, creds.UserID, newHash, now); err != nil {
				return err
			}
		}
		mfa, err := identitypg.LoadMFA(ctx, tx, p.UserID, false)
		if err != nil && !errors.Is(err, postgres.ErrNotFound) {
			return err
		}
		enabled := err == nil && mfa.Enabled
		if enabled || identity.RequiresMFA(p.Roles) {
			token, tokenHash := newToken()
			c := &Challenge{Token: token, ExpiresAt: now.Add(a.cfg.ChallengeTTL), Enroll: !enabled}
			if err := identitypg.InsertChallenge(ctx, tx, tokenHash, p.UserID, c.Enroll, now, c.ExpiresAt); err != nil {
				return err
			}
			out.Challenge = c
			return a.audit(ctx, tx, p.Actor(), "auth.login_challenge", "user", string(p.UserID), audit.ResultSuccess, "",
				map[string]string{"enroll": fmt.Sprint(c.Enroll)})
		}
		l, err := a.startSession(ctx, tx, p, previousToken, false, map[string]string{})
		out.Session = &l
		return err
	})
	if errors.Is(err, identity.ErrAccountLocked) || errors.Is(err, identity.ErrNotPrincipal) {
		return LoginResult{}, identity.ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("login %s: %w", creds.UserID, err)
	}
	return out, nil
}

func denyReason(passwordOK bool, creds identitypg.Credentials) string {
	switch {
	case !passwordOK:
		return "wrong password"
	case creds.Status != identity.UserActive:
		return "account locked"
	case !creds.EmailVerified:
		return "email not verified"
	}
	return ""
}

func (a *Auth) loadPrincipal(ctx context.Context, tx pgx.Tx, id identity.UserID) (identity.Principal, error) {
	m, _, err := identitypg.ActiveMatrix(ctx, tx)
	if err != nil {
		return identity.Principal{}, err
	}
	return identitypg.LoadPrincipal(ctx, tx, id, m)
}

// startSession revokes the session the client presented, if any, stores a
// new one and audits the login.
func (a *Auth) startSession(ctx context.Context, tx pgx.Tx, p identity.Principal, previousToken string, mfa bool, refs map[string]string) (Login, error) {
	now := a.svc.now().UTC()
	if prev, ok := parseToken(previousToken); ok {
		old, err := identitypg.SessionByTokenHash(ctx, tx, prev)
		if err != nil && !errors.Is(err, postgres.ErrNotFound) {
			return Login{}, err
		}
		if err == nil {
			revoked, err := identitypg.RevokeSession(ctx, tx, old.ID, identity.RevokeLoginRotation, now)
			if err != nil {
				return Login{}, err
			}
			if revoked {
				refs["replaced_session_id"] = old.ID
			}
		}
	}
	token, tokenHash := newToken()
	s := identity.NewSession(a.svc.newID(), p.UserID, p.Roles, a.cfg.Sessions, mfa, now)
	if err := identitypg.InsertSession(ctx, tx, s, tokenHash); err != nil {
		return Login{}, err
	}
	refs["session_id"] = s.ID
	if err := a.audit(ctx, tx, p.Actor(), "auth.login", "user", string(p.UserID), audit.ResultSuccess, "", refs); err != nil {
		return Login{}, err
	}
	return Login{Token: token, Session: s, Principal: p}, nil
}

// Authenticate resolves the principal behind a session token. Unknown,
// revoked, expired and idle sessions, sessions without the second factor
// the user's roles now require, and locked or deleted users, are all
// identity.ErrNotPrincipal. Permissions are recomputed on every call.
func (a *Auth) Authenticate(ctx context.Context, token string) (identity.Principal, identity.Session, error) {
	tokenHash, ok := parseToken(token)
	if !ok {
		return identity.Principal{}, identity.Session{}, identity.ErrNotPrincipal
	}
	s, err := identitypg.SessionByTokenHash(ctx, a.svc.pool, tokenHash)
	if errors.Is(err, postgres.ErrNotFound) {
		return identity.Principal{}, identity.Session{}, identity.ErrNotPrincipal
	}
	if err != nil {
		return identity.Principal{}, identity.Session{}, fmt.Errorf("authenticate: %w", err)
	}
	p, err := a.svc.Principal(ctx, s.UserID)
	if errors.Is(err, identity.ErrAccountLocked) || errors.Is(err, identity.ErrNotPrincipal) {
		return identity.Principal{}, identity.Session{}, fmt.Errorf("session %s: %w", s.ID, identity.ErrNotPrincipal)
	}
	if err != nil {
		return identity.Principal{}, identity.Session{}, fmt.Errorf("authenticate: %w", err)
	}
	now := a.svc.now().UTC()
	if err := s.Check(now, p.Roles, a.cfg.Sessions); err != nil {
		return identity.Principal{}, identity.Session{}, err
	}
	if now.Sub(s.LastSeenAt) >= touchInterval {
		if err := identitypg.TouchSession(ctx, a.svc.pool, s.ID, now); err != nil {
			return identity.Principal{}, identity.Session{}, fmt.Errorf("authenticate: %w", err)
		}
		s.LastSeenAt = now
	}
	return p, s, nil
}

// Logout revokes the session behind token. It is idempotent: an unknown or
// already revoked token is not an error.
func (a *Auth) Logout(ctx context.Context, token string) error {
	tokenHash, ok := parseToken(token)
	if !ok {
		return nil
	}
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		s, err := identitypg.SessionByTokenHash(ctx, tx, tokenHash)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		revoked, err := identitypg.RevokeSession(ctx, tx, s.ID, identity.RevokeLogout, a.svc.now())
		if err != nil || !revoked {
			return err
		}
		return a.audit(ctx, tx, identity.UserActor(s.UserID), "auth.logout", "user", string(s.UserID), audit.ResultSuccess, "",
			map[string]string{"session_id": s.ID})
	})
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

// ChangePassword replaces the principal's password after checking the
// current one. Every other session of the user is revoked; the session the
// change was made from (keepSession) stays.
func (a *Auth) ChangePassword(ctx context.Context, p identity.Principal, keepSession, current, next string) error {
	if p.UserID == "" {
		return identity.ErrNotPrincipal
	}
	if err := a.cfg.Credentials.CheckPassword("new_password", next); err != nil {
		return err
	}
	creds, err := identitypg.CredentialsByID(ctx, a.svc.pool, p.UserID)
	if err != nil {
		return fmt.Errorf("change password of %s: %w", p.UserID, err)
	}
	ok := false
	if creds.PasswordHash != "" && utf8.RuneCountInString(current) <= identity.MaxPasswordLength {
		if ok, _, err = a.hasher.Verify(ctx, current, creds.PasswordHash); err != nil {
			return fmt.Errorf("change password of %s: %w", p.UserID, err)
		}
	}
	if !ok {
		return domainerr.Invalid("current_password", "is incorrect")
	}
	hash, err := a.hasher.Hash(ctx, next)
	if err != nil {
		return fmt.Errorf("change password of %s: %w", p.UserID, err)
	}
	err = postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		now := a.svc.now().UTC()
		if err := a.replacePassword(ctx, tx, p.UserID, hash, keepSession, identity.RevokePasswordChange, now); err != nil {
			return err
		}
		return a.audit(ctx, tx, p.Actor(), "user.password_change", "user", string(p.UserID), audit.ResultSuccess, "", nil)
	})
	if err != nil {
		return fmt.Errorf("change password of %s: %w", p.UserID, err)
	}
	return nil
}

// RequestPasswordReset emails a single-use reset link when email belongs to
// an active account, and otherwise does nothing; the caller always gets the
// same answer.
func (a *Auth) RequestPasswordReset(ctx context.Context, email string) error {
	normalized, err := identity.NormalizeEmail(email)
	if err != nil {
		return domainerr.Invalid("email", "must be a valid email address")
	}
	creds, err := identitypg.CredentialsByEmail(ctx, a.svc.pool, normalized)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && creds.Status != identity.UserActive) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("request password reset: %w", err)
	}
	token, tokenHash := newToken()
	now := a.svc.now().UTC()
	expiresAt := now.Add(a.cfg.ResetTokenTTL)
	err = postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		if err := identitypg.InsertToken(ctx, tx, identitypg.TokenPasswordReset, tokenHash, creds.UserID, now, expiresAt); err != nil {
			return err
		}
		return a.audit(ctx, tx, identity.SystemActor("password-reset"), "user.password_reset_request", "user", string(creds.UserID),
			audit.ResultSuccess, "", nil)
	})
	if err != nil {
		return fmt.Errorf("request password reset: %w", err)
	}
	a.send(ctx, "password-reset", creds.UserID, func() error { return a.notify.PasswordReset(ctx, normalized, token, expiresAt) })
	return nil
}

// ResetPassword sets a new password with a reset token. The token works
// once; using it also invalidates the user's other reset tokens, ends
// every session and, since the link proves control of the mailbox,
// verifies the email.
func (a *Auth) ResetPassword(ctx context.Context, token, next string) error {
	if err := a.cfg.Credentials.CheckPassword("new_password", next); err != nil {
		return err
	}
	tokenHash, ok := parseToken(token)
	if !ok {
		return identity.ErrInvalidResetToken
	}
	// Hash outside the transaction so no row lock is held meanwhile.
	hash, err := a.hasher.Hash(ctx, next)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	err = postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		t, err := identitypg.TokenForUpdate(ctx, tx, identitypg.TokenPasswordReset, tokenHash)
		if errors.Is(err, postgres.ErrNotFound) {
			return identity.ErrInvalidResetToken
		}
		if err != nil {
			return err
		}
		now := a.svc.now().UTC()
		if t.Used || !now.Before(t.ExpiresAt) {
			return identity.ErrInvalidResetToken
		}
		creds, err := identitypg.CredentialsByID(ctx, tx, t.UserID)
		if err != nil {
			return err
		}
		if creds.Status != identity.UserActive {
			return identity.ErrInvalidResetToken
		}
		if err := a.replacePassword(ctx, tx, t.UserID, hash, "", identity.RevokePasswordReset, now); err != nil {
			return err
		}
		if err := identitypg.MarkEmailVerified(ctx, tx, t.UserID, now); err != nil {
			return err
		}
		return a.audit(ctx, tx, identity.UserActor(t.UserID), "user.password_reset", "user", string(t.UserID), audit.ResultSuccess, "", nil)
	})
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

// BootstrapAdmin creates the first administrator, with the email treated
// as verified (the operator supplies it). It refuses while any active
// administrator exists, so it cannot be used to add more; further admins
// are granted through GrantRole. The admin enrols TOTP at first login.
func (a *Auth) BootstrapAdmin(ctx context.Context, email, password string) (identity.UserID, error) {
	normalized, err := identity.NormalizeEmail(email)
	if err != nil {
		return "", domainerr.Invalid("email", "must be a valid email address")
	}
	if err := a.cfg.Credentials.CheckPassword("password", password); err != nil {
		return "", err
	}
	hash, err := a.hasher.Hash(ctx, password)
	if err != nil {
		return "", fmt.Errorf("bootstrap admin: %w", err)
	}
	now := a.svc.now().UTC()
	u := identity.User{ID: identity.UserID(a.svc.newID()), Email: normalized, Status: identity.UserActive,
		Roles: []identity.Role{identity.RoleAdmin}, EmailVerifiedAt: &now, CreatedAt: now}
	system := identity.SystemActor("bootstrap-admin")
	err = postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		if err := identitypg.LockAdminSet(ctx, tx); err != nil {
			return err
		}
		n, err := identitypg.CountActiveAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if n > 0 {
			return identity.ErrAdminExists
		}
		if err := identitypg.InsertUser(ctx, tx, u, hash, system); err != nil {
			return err
		}
		return a.audit(ctx, tx, system, "user.bootstrap_admin", "user", string(u.ID), audit.ResultSuccess, "", nil)
	})
	if err != nil {
		return "", fmt.Errorf("bootstrap admin: %w", err)
	}
	return u.ID, nil
}

func (a *Auth) replacePassword(ctx context.Context, tx pgx.Tx, id identity.UserID, hash, keepSession string, reason identity.RevokeReason, now time.Time) error {
	if err := identitypg.SetPasswordHash(ctx, tx, id, hash, now); err != nil {
		return err
	}
	if err := identitypg.ConsumeTokens(ctx, tx, identitypg.TokenPasswordReset, id, now); err != nil {
		return err
	}
	_, err := identitypg.RevokeUserSessions(ctx, tx, id, keepSession, reason, now)
	return err
}

// send hands a message to the notifier; a failure (e.g. a full queue) is
// logged and never fails the use case.
func (a *Auth) send(ctx context.Context, kind string, user identity.UserID, fn func() error) {
	if err := fn(); err != nil {
		slog.WarnContext(ctx, "account email not sent", "kind", kind, "user_id", user, "error", err)
	}
}

func (a *Auth) audit(ctx context.Context, tx pgx.Tx, actor identity.Actor, action, resourceType, resourceID string,
	result audit.Result, reason string, refs map[string]string) error {
	return auditpg.Append(ctx, tx, audit.Event{
		ID: a.svc.newID(), OccurredAt: a.svc.now().UTC(), Actor: actor, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Result: result, Reason: reason, References: refs,
	})
}

// dummyVerify burns the cost of one password check.
func (a *Auth) dummyVerify(ctx context.Context, password string) {
	a.dummyOnce.Do(func() {
		// Not the request context: a cancelled first request must not
		// leave every later one without the dummy hash.
		h, err := a.hasher.Hash(context.Background(), "timing-equalisation-only")
		if err != nil {
			slog.WarnContext(ctx, "dummy password hash failed", "error", err)
		}
		a.dummyHash = h
	})
	if a.dummyHash != "" {
		_, _, _ = a.hasher.Verify(ctx, password, a.dummyHash)
	}
}

// tokenBytes is the entropy of session, challenge and emailed tokens (256
// bits).
const tokenBytes = 32

var tokenEncoding = base64.RawURLEncoding

// newToken returns a random bearer token and the SHA-256 hash under which
// it is stored. A fast hash is enough: the token is random, not a password.
func newToken() (string, []byte) {
	b := make([]byte, tokenBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	token := tokenEncoding.EncodeToString(b)
	return token, hashToken(token)
}

// parseToken checks a client token's shape before hashing it.
func parseToken(token string) ([]byte, bool) {
	if len(token) != tokenEncoding.EncodedLen(tokenBytes) {
		return nil, false
	}
	if _, err := tokenEncoding.DecodeString(token); err != nil {
		return nil, false
	}
	return hashToken(token), true
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
