package httpapi

import (
	"context"
	"net/http"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/platform/ratelimit"
)

// cookieName uses the __Host- prefix when Secure, which makes browsers
// refuse the cookie unless it is Secure, host-only and Path=/.
func (s *Server) cookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-session"
	}
	return "session"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", Expires: expires,
		MaxAge: int(expires.Sub(s.cfg.Now()).Seconds()), Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1, Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) presentedSession(r *http.Request) string {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		return c.Value
	}
	return ""
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type emailRequest struct {
	Email string `json:"email"`
}

type tokenRequest struct {
	Token string `json:"token"`
}

type acceptedResponse struct {
	Status string `json:"status"`
}

func accepted(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, acceptedResponse{Status: "accepted"})
}

// register always answers 202 for valid input, whether or not the email
// was already registered; the account works once the emailed link is used.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "register-ip", s.clientIP(r), s.cfg.RateLimits.RegisterPerIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req credentialsRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.Register(r.Context(), req.Email, req.Password); err != nil {
		writeError(w, r, err)
		return
	}
	accepted(w)
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "verify-ip", s.clientIP(r), s.cfg.RateLimits.VerifyConfirmPerIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req tokenRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.VerifyEmail(r.Context(), req.Token); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resendVerification always answers 202 for a well-formed email.
func (s *Server) resendVerification(w http.ResponseWriter, r *http.Request) {
	s.emailAction(w, r, "verify-resend", s.cfg.RateLimits.VerifyResendPerIP, s.cfg.RateLimits.VerifyResendPerEmail, s.auth.ResendVerification)
}

// requestPasswordReset always answers 202 for a well-formed email.
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	s.emailAction(w, r, "reset", s.cfg.RateLimits.ResetPerIP, s.cfg.RateLimits.ResetPerEmail, s.auth.RequestPasswordReset)
}

// emailAction runs an "email me a link" use case. Over the per-email limit
// it answers as usual but sends nothing, so the limit cannot be used to
// probe for accounts.
func (s *Server) emailAction(w http.ResponseWriter, r *http.Request, bucket string, perIP, perEmail ratelimit.Rate, run func(context.Context, string) error) {
	if err := s.limit(r, bucket+"-ip", s.clientIP(r), perIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req emailRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	err := s.limit(r, bucket+"-email", emailKey(req.Email), perEmail)
	if err != nil && !isRateLimited(err) {
		writeError(w, r, err)
		return
	}
	if err == nil {
		if err := run(r.Context(), req.Email); err != nil {
			writeError(w, r, err)
			return
		}
	}
	accepted(w)
}

type sessionResponse struct {
	Status        string                `json:"status"`
	UserID        identity.UserID       `json:"user_id"`
	Roles         []identity.Role       `json:"roles"`
	Permissions   []identity.Permission `json:"permissions"`
	MFA           bool                  `json:"mfa"`
	ExpiresAt     time.Time             `json:"expires_at"`
	RecoveryCodes []string              `json:"recovery_codes,omitempty"`
}

func newSessionResponse(p identity.Principal, sess identity.Session) sessionResponse {
	roles := p.Roles
	if roles == nil {
		roles = []identity.Role{}
	}
	return sessionResponse{Status: "authenticated", UserID: p.UserID, Roles: roles, Permissions: p.Permissions(),
		MFA: sess.MFAAt != nil, ExpiresAt: sess.ExpiresAt}
}

type challengeResponse struct {
	// Status is mfa_required (enter a code) or mfa_enrollment_required
	// (call login/mfa/enroll first).
	Status    string    `json:"status"`
	Challenge string    `json:"challenge"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "login-ip", s.clientIP(r), s.cfg.RateLimits.LoginPerIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req credentialsRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.limit(r, "login-email", emailKey(req.Email), s.cfg.RateLimits.LoginPerEmail); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.auth.Login(r.Context(), req.Email, req.Password, s.presentedSession(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if c := res.Challenge; c != nil {
		status := "mfa_required"
		if c.Enroll {
			status = "mfa_enrollment_required"
		}
		writeJSON(w, http.StatusOK, challengeResponse{Status: status, Challenge: c.Token, ExpiresAt: c.ExpiresAt})
		return
	}
	s.issueSession(w, *res.Session)
}

func (s *Server) issueSession(w http.ResponseWriter, l identityapp.Login) {
	s.setSessionCookie(w, l.Token, l.Session.ExpiresAt)
	resp := newSessionResponse(l.Principal, l.Session)
	resp.RecoveryCodes = l.RecoveryCodes
	writeJSON(w, http.StatusOK, resp)
}

type challengeRequest struct {
	Challenge string `json:"challenge"`
}

type mfaCodeRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

type enrollmentResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
}

const maxCodeLength = 32

func checkCode(code string) error {
	if code == "" || len(code) > maxCodeLength {
		return domainerr.Invalid("code", "required, at most 32 characters")
	}
	return nil
}

// loginEnroll starts the TOTP enrolment a challenged login requires.
func (s *Server) loginEnroll(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "mfa-ip", s.clientIP(r), s.cfg.RateLimits.MFAPerIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req challengeRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	e, err := s.auth.BeginLoginEnrollment(r.Context(), req.Challenge)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, enrollmentResponse{Secret: e.Secret, OTPAuthURI: e.URI})
}

// loginMFA completes a challenged login and sets the session cookie.
func (s *Server) loginMFA(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "mfa-ip", s.clientIP(r), s.cfg.RateLimits.MFAPerIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req mfaCodeRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	if err := checkCode(req.Code); err != nil {
		writeError(w, r, err)
		return
	}
	l, err := s.auth.CompleteLogin(r.Context(), req.Challenge, req.Code, s.presentedSession(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.issueSession(w, l)
}

// logout needs no live session: it clears the cookie either way.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := s.presentedSession(r); token != "" {
		if err := s.auth.Logout(r.Context(), token); err != nil {
			writeError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, p identity.Principal, sess identity.Session) {
	writeJSON(w, http.StatusOK, newSessionResponse(p, sess))
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, p identity.Principal, sess identity.Session) {
	if err := s.limit(r, "password-user", string(p.UserID), s.cfg.RateLimits.PasswordPerUser); err != nil {
		writeError(w, r, err)
		return
	}
	var req changePasswordRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.ChangePassword(r.Context(), p, sess.ID, req.CurrentPassword, req.NewPassword); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type resetConfirmRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "reset-confirm-ip", s.clientIP(r), s.cfg.RateLimits.ResetConfirmIP); err != nil {
		writeError(w, r, err)
		return
	}
	var req resetConfirmRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mfaSetup starts voluntary TOTP enrolment for the signed-in user.
func (s *Server) mfaSetup(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	if err := s.limit(r, "mfa-user", string(p.UserID), s.cfg.RateLimits.MFAPerUser); err != nil {
		writeError(w, r, err)
		return
	}
	e, err := s.auth.StartMFASetup(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, enrollmentResponse{Secret: e.Secret, OTPAuthURI: e.URI})
}

type codeRequest struct {
	Code string `json:"code"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (s *Server) decodeCode(w http.ResponseWriter, r *http.Request, p identity.Principal) (string, error) {
	if err := s.limit(r, "mfa-user", string(p.UserID), s.cfg.RateLimits.MFAPerUser); err != nil {
		return "", err
	}
	var req codeRequest
	if err := decodeJSON(w, r, &req, smallBody); err != nil {
		return "", err
	}
	return req.Code, checkCode(req.Code)
}

func (s *Server) mfaSetupConfirm(w http.ResponseWriter, r *http.Request, p identity.Principal, sess identity.Session) {
	code, err := s.decodeCode(w, r, p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	codes, err := s.auth.ConfirmMFASetup(r.Context(), p, sess.ID, code)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

func (s *Server) mfaDisable(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	code, err := s.decodeCode(w, r, p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.DisableMFA(r.Context(), p, code); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
