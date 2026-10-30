DROP TABLE rate_limit_windows;
DROP TABLE login_challenges;
DROP TABLE mfa_recovery_codes;
DROP TABLE user_mfa;
ALTER TABLE sessions DROP COLUMN mfa_at;
DROP TABLE email_verification_tokens;
ALTER TABLE users DROP COLUMN email_verified_at;
