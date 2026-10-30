-- Decision D23 (A37/A38 confirmed 2026-10-30): mandatory email
-- verification before login, TOTP second factor (mandatory for admin and
-- finance), and rate-limit counters shared by every API instance.

-- NULL = not verified; login is refused until set. Accounts that existed
-- before verification (created by tests or bootstrap tooling, before any
-- public registration) are treated as verified.
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
UPDATE users SET email_verified_at = created_at;

-- Single-use, expiring email verification tokens, stored hashed.
CREATE TABLE email_verification_tokens (
    token_hash bytea PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    used_at    timestamptz
);
CREATE INDEX email_verification_tokens_user ON email_verification_tokens (user_id) WHERE used_at IS NULL;

-- When the session's login passed a second factor; NULL = password only.
-- Users whose roles require MFA are refused on sessions without it.
ALTER TABLE sessions ADD COLUMN mfa_at timestamptz;

-- TOTP enrolment. The secret is encrypted with AUTH_MFA_ENCRYPTION_KEY
-- (AES-256-GCM, bound to user_id); enabled_at NULL = enrolment started but
-- not confirmed. last_used_step blocks replay of a code already used.
CREATE TABLE user_mfa (
    user_id           text PRIMARY KEY REFERENCES users (id),
    secret_ciphertext bytea NOT NULL,
    created_at        timestamptz NOT NULL,
    enabled_at        timestamptz,
    last_used_step    bigint NOT NULL DEFAULT 0
);

-- Single-use recovery codes, stored hashed.
CREATE TABLE mfa_recovery_codes (
    user_id    text NOT NULL REFERENCES users (id),
    code_hash  bytea NOT NULL,
    created_at timestamptz NOT NULL,
    used_at    timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

-- Second step of a login: issued after a correct password when a second
-- factor (or its enrolment) is needed. Short-lived, single-use, attempt
-- limited.
CREATE TABLE login_challenges (
    token_hash bytea PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    enroll     boolean NOT NULL,
    attempts   integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    used_at    timestamptz
);

-- Fixed-window rate-limit counters. Keys are hashed because they contain
-- emails and IP addresses.
CREATE TABLE rate_limit_windows (
    key_hash     bytea PRIMARY KEY,
    window_start timestamptz NOT NULL,
    hits         integer NOT NULL CHECK (hits > 0)
);
CREATE INDEX rate_limit_windows_start ON rate_limit_windows (window_start);
