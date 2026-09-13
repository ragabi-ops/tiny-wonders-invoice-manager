-- 0001_foundation.sql
-- Phase 1: users, roles, sessions, audit trail, regulatory configuration,
-- business profile. Money is never stored here; later phases store it as
-- BIGINT agorot. Timestamps are UTC (TIMESTAMPTZ); business dates are DATE
-- already interpreted in Asia/Jerusalem.

-- ---------------------------------------------------------------- helpers --

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Guards append-only tables. Blocks UPDATE and DELETE at the database level so
-- application bugs cannot rewrite history (plan.md 3.5, 16).
CREATE OR REPLACE FUNCTION deny_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'table % is append-only: % is not permitted',
        TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

-- ------------------------------------------------------------------ users --

CREATE TABLE users (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email                 text        NOT NULL,
    display_name          text        NOT NULL CHECK (length(btrim(display_name)) > 0),
    password_hash         text        NOT NULL,
    active                boolean     NOT NULL DEFAULT true,
    must_change_password  boolean     NOT NULL DEFAULT false,
    failed_login_count    integer     NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
    locked_until          timestamptz,
    last_login_at         timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- Email is the login identifier and is compared case-insensitively.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE user_roles (
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role        text        NOT NULL CHECK (role IN ('OWNER', 'OPERATOR', 'ACCOUNTANT', 'READ_ONLY')),
    granted_at  timestamptz NOT NULL DEFAULT now(),
    granted_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (user_id, role)
);

CREATE INDEX user_roles_role_idx ON user_roles (role);

-- --------------------------------------------------------------- sessions --

-- Only hashes are stored: a database leak yields no usable session or CSRF
-- token (SECURITY.md 2, 3).
CREATE TABLE sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash       bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    csrf_token_hash  bytea       NOT NULL CHECK (octet_length(csrf_token_hash) = 32),
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    ip               inet,
    user_agent       text
);

CREATE INDEX sessions_user_id_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- ------------------------------------------------------------------ audit --

CREATE TABLE audit_events (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at     timestamptz NOT NULL DEFAULT now(),
    actor_user_id   uuid        REFERENCES users (id) ON DELETE SET NULL,
    actor_email     text,
    operation       text        NOT NULL CHECK (length(btrim(operation)) > 0),
    entity_type     text,
    entity_id       text,
    request_id      text,
    before_summary  jsonb,
    after_summary   jsonb,
    reason          text,
    ip              inet
);

CREATE INDEX audit_events_occurred_at_idx ON audit_events (occurred_at DESC);
CREATE INDEX audit_events_entity_idx      ON audit_events (entity_type, entity_id);
CREATE INDEX audit_events_actor_idx       ON audit_events (actor_user_id, occurred_at DESC);
CREATE INDEX audit_events_operation_idx   ON audit_events (operation, occurred_at DESC);

CREATE TRIGGER audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();

-- --------------------------------------------- regulatory configuration ----

-- Effective-dated. Resolution is "greatest effective_from <= business date".
-- Nothing regulatory belongs in Go constants (plan.md 3.7).
CREATE TABLE regulatory_config (
    key             text        NOT NULL,
    effective_from  date        NOT NULL,
    value           jsonb       NOT NULL,
    note            text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    created_by      uuid        REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (key, effective_from)
);

-- Regulatory history must not be rewritten in place; supersede it with a new
-- effective_from row instead.
CREATE TRIGGER regulatory_config_append_only
    BEFORE UPDATE OR DELETE ON regulatory_config
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();

-- ------------------------------------------------------- business profile --

-- Single row. Mutable, because it describes the business *now*; every issued
-- document snapshots these values at issuance time (plan.md 11).
CREATE TABLE business_profile (
    id                 boolean PRIMARY KEY DEFAULT true CHECK (id),
    legal_name         text        NOT NULL DEFAULT '',
    display_name       text        NOT NULL DEFAULT '',
    business_number    text        NOT NULL DEFAULT '',
    address            text        NOT NULL DEFAULT '',
    phone              text        NOT NULL DEFAULT '',
    email              text        NOT NULL DEFAULT '',
    website            text        NOT NULL DEFAULT '',
    bank_details       text        NOT NULL DEFAULT '',
    footer_note        text        NOT NULL DEFAULT '',
    updated_at         timestamptz NOT NULL DEFAULT now(),
    updated_by         uuid        REFERENCES users (id) ON DELETE SET NULL
);

CREATE TRIGGER business_profile_set_updated_at
    BEFORE UPDATE ON business_profile
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

INSERT INTO business_profile (id) VALUES (true);
