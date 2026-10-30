-- Foundation: extensions, identity, audit, versioned policy documents and
-- store hours. Money is bigint VND, timestamps are timestamptz (UTC
-- instants; business interpretation in Asia/Ho_Chi_Minh happens in Go).

-- btree_gist lets the allocation exclusion constraint combine equality on
-- asset_id with range overlap.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- History tables are append-only: corrections are new rows, never edits.
CREATE FUNCTION reject_history_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END;
$$;

CREATE TABLE users (
    id            text PRIMARY KEY,
    email         text NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    -- Encoded Argon2id/bcrypt hash; the password itself is never stored.
    password_hash text,
    status        text NOT NULL CHECK (status IN ('ACTIVE', 'LOCKED')),
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE user_roles (
    user_id         text NOT NULL REFERENCES users (id),
    role            text NOT NULL CHECK (role IN ('customer', 'rental_staff', 'inventory_manager', 'finance', 'admin')),
    granted_at      timestamptz NOT NULL,
    granted_by_kind text NOT NULL CHECK (granted_by_kind IN ('user', 'system')),
    granted_by_id   text NOT NULL,
    PRIMARY KEY (user_id, role)
);

-- Server-side sessions. Only a hash of the random token is stored so a
-- database leak does not leak usable sessions.
CREATE TABLE sessions (
    id            text PRIMARY KEY,
    token_hash    bytea NOT NULL UNIQUE,
    user_id       text NOT NULL REFERENCES users (id),
    created_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL CHECK (expires_at > created_at),
    last_seen_at  timestamptz,
    revoked_at    timestamptz,
    revoke_reason text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_live_by_user ON sessions (user_id) WHERE revoked_at IS NULL;

-- Single-use, expiring password reset tokens, stored hashed.
CREATE TABLE password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    used_at    timestamptz
);

CREATE TABLE audit_events (
    id            text PRIMARY KEY,
    occurred_at   timestamptz NOT NULL,
    actor_kind    text NOT NULL CHECK (actor_kind IN ('user', 'system')),
    actor_id      text NOT NULL,
    action        text NOT NULL,
    resource_type text NOT NULL,
    resource_id   text NOT NULL,
    result        text NOT NULL CHECK (result IN ('SUCCESS', 'DENIED', 'FAILED')),
    reason        text NOT NULL DEFAULT '',
    refs          jsonb NOT NULL DEFAULT '{}',
    changes       jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_events_resource ON audit_events (resource_type, resource_id, occurred_at);
CREATE INDEX audit_events_time ON audit_events (occurred_at);
CREATE TRIGGER audit_events_append_only BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();

-- Versioned policy documents (rental policy, allocation policy). A version
-- is immutable once written; activating another version only moves the
-- active flag. Bookings keep their own snapshot of the rental policy.
CREATE TABLE policy_documents (
    kind            text NOT NULL CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION')),
    version         text NOT NULL CHECK (version <> ''),
    document        jsonb NOT NULL,
    created_at      timestamptz NOT NULL,
    created_by_kind text NOT NULL CHECK (created_by_kind IN ('user', 'system')),
    created_by_id   text NOT NULL,
    active          boolean NOT NULL DEFAULT false,
    PRIMARY KEY (kind, version)
);
CREATE UNIQUE INDEX policy_documents_one_active ON policy_documents (kind) WHERE active;

CREATE FUNCTION reject_policy_document_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'policy document %/% cannot be deleted', OLD.kind, OLD.version
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF NEW.document IS DISTINCT FROM OLD.document OR NEW.kind <> OLD.kind OR NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'policy document %/% is immutable', OLD.kind, OLD.version
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER policy_documents_immutable BEFORE UPDATE OR DELETE ON policy_documents
    FOR EACH ROW EXECUTE FUNCTION reject_policy_document_change();

-- Store hours as local wall-clock times (Asia/Ho_Chi_Minh). COUNTER slots
-- are pickup/return appointments at the store; DELIVERY slots are courier
-- delivery/pickup windows. weekday follows Go's time.Weekday (0 = Sunday).
CREATE TABLE store_slots (
    kind      text NOT NULL CHECK (kind IN ('COUNTER', 'DELIVERY')),
    weekday   smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    slot_id   text NOT NULL CHECK (slot_id <> ''),
    starts_at time NOT NULL,
    ends_at   time NOT NULL CHECK (ends_at > starts_at),
    PRIMARY KEY (kind, weekday, slot_id)
);

-- Local dates the store is closed (holidays, closures).
CREATE TABLE store_closures (
    closed_on date PRIMARY KEY,
    reason    text NOT NULL DEFAULT ''
);
