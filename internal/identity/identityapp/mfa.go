package identityapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/identity/totp"
	"webee_rentor/internal/platform/postgres"
)

// Enrollment is a new TOTP secret to load into an authenticator app,
// typed (Secret) or scanned (URI as a QR code). It is shown once.
type Enrollment struct {
	Secret string
	URI    string
}

// BeginLoginEnrollment starts TOTP enrolment for a login challenge issued
// because the account's roles require a second factor it does not have
// yet. Calling it again replaces the unconfirmed secret.
func (a *Auth) BeginLoginEnrollment(ctx context.Context, challengeToken string) (Enrollment, error) {
	tokenHash, ok := parseToken(challengeToken)
	if !ok {
		return Enrollment{}, identity.ErrInvalidChallenge
	}
	var out Enrollment
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		c, err := a.liveChallenge(ctx, tx, tokenHash)
		if err != nil {
			return err
		}
		if !c.Enroll {
			return identity.ErrMFAAlreadyEnabled
		}
		out, err = a.startEnrollment(ctx, tx, c.UserID)
		return err
	})
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin login enrolment: %w", err)
	}
	return out, nil
}

// CompleteLogin finishes a challenged login with a TOTP code, or with a
// single-use recovery code once TOTP is enabled. An enrolment challenge
// confirms the new secret and returns fresh recovery codes. Each wrong
// code counts against the challenge; after maxChallengeAttempts the user
// must start over with the password.
func (a *Auth) CompleteLogin(ctx context.Context, challengeToken, code, previousToken string) (Login, error) {
	tokenHash, ok := parseToken(challengeToken)
	if !ok {
		return Login{}, identity.ErrInvalidChallenge
	}
	var out Login
	var codeErr error
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		c, err := a.liveChallenge(ctx, tx, tokenHash)
		if err != nil {
			return err
		}
		mfa, err := identitypg.LoadMFA(ctx, tx, c.UserID, true)
		if errors.Is(err, postgres.ErrNotFound) {
			if c.Enroll {
				return identity.ErrMFANotEnabled // enrolment was not started
			}
			return identity.ErrInvalidChallenge // MFA was reset meanwhile
		}
		if err != nil {
			return err
		}
		now := a.svc.now().UTC()
		method, err := a.checkSecondFactor(ctx, tx, c.UserID, mfa, code, now)
		if errors.Is(err, identity.ErrInvalidMFACode) {
			// Commit the attempt so guesses stay counted.
			codeErr = err
			if err := identitypg.CountChallengeAttempt(ctx, tx, tokenHash, c.Attempts+1 >= maxChallengeAttempts, now); err != nil {
				return err
			}
			return a.audit(ctx, tx, identity.SystemActor("login"), "auth.mfa", "user", string(c.UserID), audit.ResultDenied, "wrong code", nil)
		}
		if err != nil {
			return err
		}
		if err := identitypg.CountChallengeAttempt(ctx, tx, tokenHash, true, now); err != nil {
			return err
		}
		var codes []string
		if !mfa.Enabled {
			if codes, err = a.enable(ctx, tx, c.UserID, now); err != nil {
				return err
			}
		}
		p, err := a.loadPrincipal(ctx, tx, c.UserID)
		if err != nil {
			return err
		}
		out, err = a.startSession(ctx, tx, p, previousToken, true, map[string]string{"mfa_method": method})
		out.RecoveryCodes = codes
		return err
	})
	if errors.Is(err, identity.ErrAccountLocked) || errors.Is(err, identity.ErrNotPrincipal) {
		return Login{}, identity.ErrInvalidChallenge
	}
	if err != nil {
		return Login{}, fmt.Errorf("complete login: %w", err)
	}
	if codeErr != nil {
		return Login{}, codeErr
	}
	return out, nil
}

// StartMFASetup lets a signed-in user enrol TOTP voluntarily.
func (a *Auth) StartMFASetup(ctx context.Context, p identity.Principal) (Enrollment, error) {
	if p.UserID == "" {
		return Enrollment{}, identity.ErrNotPrincipal
	}
	var out Enrollment
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		var err error
		out, err = a.startEnrollment(ctx, tx, p.UserID)
		return err
	})
	if err != nil {
		return Enrollment{}, fmt.Errorf("start mfa setup of %s: %w", p.UserID, err)
	}
	return out, nil
}

// ConfirmMFASetup enables TOTP after the user proves the app works, marks
// the current session as second-factor verified, ends the user's other
// sessions and returns recovery codes.
func (a *Auth) ConfirmMFASetup(ctx context.Context, p identity.Principal, sessionID, code string) ([]string, error) {
	if p.UserID == "" {
		return nil, identity.ErrNotPrincipal
	}
	var codes []string
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		mfa, err := identitypg.LoadMFA(ctx, tx, p.UserID, true)
		if errors.Is(err, postgres.ErrNotFound) {
			return identity.ErrMFANotEnabled
		}
		if err != nil {
			return err
		}
		if mfa.Enabled {
			return identity.ErrMFAAlreadyEnabled
		}
		now := a.svc.now().UTC()
		if _, err := a.checkSecondFactor(ctx, tx, p.UserID, mfa, code, now); err != nil {
			return err
		}
		if codes, err = a.enable(ctx, tx, p.UserID, now); err != nil {
			return err
		}
		if err := identitypg.SetSessionMFA(ctx, tx, sessionID, now); err != nil {
			return err
		}
		_, err = identitypg.RevokeUserSessions(ctx, tx, p.UserID, sessionID, identity.RevokeMFAChange, now)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("confirm mfa setup of %s: %w", p.UserID, err)
	}
	return codes, nil
}

// DisableMFA turns TOTP off with a current code. Users whose roles require
// a second factor cannot.
func (a *Auth) DisableMFA(ctx context.Context, p identity.Principal, code string) error {
	if p.UserID == "" {
		return identity.ErrNotPrincipal
	}
	if identity.RequiresMFA(p.Roles) {
		return identity.ErrMFARequired
	}
	err := postgres.InTx(ctx, a.svc.pool, func(tx pgx.Tx) error {
		mfa, err := identitypg.LoadMFA(ctx, tx, p.UserID, true)
		if errors.Is(err, postgres.ErrNotFound) || (err == nil && !mfa.Enabled) {
			return identity.ErrMFANotEnabled
		}
		if err != nil {
			return err
		}
		if _, err := a.checkSecondFactor(ctx, tx, p.UserID, mfa, code, a.svc.now().UTC()); err != nil {
			return err
		}
		if _, err := identitypg.DeleteMFA(ctx, tx, p.UserID); err != nil {
			return err
		}
		return a.audit(ctx, tx, p.Actor(), "user.mfa_disable", "user", string(p.UserID), audit.ResultSuccess, "", nil)
	})
	if err != nil {
		return fmt.Errorf("disable mfa of %s: %w", p.UserID, err)
	}
	return nil
}

// liveChallenge locks a challenge that is unused, unexpired and has
// attempts left.
func (a *Auth) liveChallenge(ctx context.Context, tx pgx.Tx, tokenHash []byte) (identitypg.Challenge, error) {
	c, err := identitypg.ChallengeForUpdate(ctx, tx, tokenHash)
	if errors.Is(err, postgres.ErrNotFound) {
		return identitypg.Challenge{}, identity.ErrInvalidChallenge
	}
	if err != nil {
		return identitypg.Challenge{}, err
	}
	if c.Used || c.Attempts >= maxChallengeAttempts || !a.svc.now().Before(c.ExpiresAt) {
		return identitypg.Challenge{}, identity.ErrInvalidChallenge
	}
	return c, nil
}

func (a *Auth) startEnrollment(ctx context.Context, tx pgx.Tx, id identity.UserID) (Enrollment, error) {
	if a.secrets == nil {
		return Enrollment{}, identity.ErrMFAUnavailable
	}
	creds, err := identitypg.CredentialsByID(ctx, tx, id)
	if err != nil {
		return Enrollment{}, err
	}
	secret := totp.NewSecret()
	if err := identitypg.StartMFAEnrolment(ctx, tx, id, a.secrets.Seal(secret, string(id)), a.svc.now()); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Secret: totp.EncodeSecret(secret), URI: totp.URI(a.cfg.MFAIssuer, creds.Email, secret)}, nil
}

// checkSecondFactor accepts a TOTP code for a step after the last one used
// or, once enabled, an unused recovery code, recording the use. The caller
// holds the user_mfa row lock.
func (a *Auth) checkSecondFactor(ctx context.Context, tx pgx.Tx, id identity.UserID, mfa identitypg.MFA, code string, now time.Time) (string, error) {
	if hash, ok := totp.HashRecoveryCode(code); ok {
		if !mfa.Enabled {
			return "", identity.ErrInvalidMFACode
		}
		used, err := identitypg.UseRecoveryCode(ctx, tx, id, hash, now)
		if err != nil {
			return "", err
		}
		if !used {
			return "", identity.ErrInvalidMFACode
		}
		return "recovery_code", nil
	}
	if a.secrets == nil {
		return "", identity.ErrMFAUnavailable
	}
	secret, err := a.secrets.Open(mfa.SecretCiphertext, string(id))
	if err != nil {
		return "", fmt.Errorf("mfa secret of %s: %w", id, err)
	}
	step, ok := totp.Verify(secret, code, now, mfa.LastUsedStep)
	if !ok {
		return "", identity.ErrInvalidMFACode
	}
	if err := identitypg.RecordMFAUse(ctx, tx, id, step, false, now); err != nil {
		return "", err
	}
	return "totp", nil
}

// enable confirms the enrolment, issues recovery codes and audits it.
func (a *Auth) enable(ctx context.Context, tx pgx.Tx, id identity.UserID, now time.Time) ([]string, error) {
	if err := identitypg.RecordMFAUse(ctx, tx, id, 0, true, now); err != nil {
		return nil, err
	}
	codes := totp.NewRecoveryCodes()
	hashes := make([][]byte, len(codes))
	for i, c := range codes {
		hashes[i], _ = totp.HashRecoveryCode(c)
	}
	if err := identitypg.ReplaceRecoveryCodes(ctx, tx, id, hashes, now); err != nil {
		return nil, err
	}
	return codes, a.audit(ctx, tx, identity.UserActor(id), "user.mfa_enable", "user", string(id), audit.ResultSuccess, "", nil)
}
