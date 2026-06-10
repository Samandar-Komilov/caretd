-- +goose Up
-- +goose StatementBegin

-- pgcrypto provides gen_random_uuid() on PostgreSQL < 13.
-- On PostgreSQL 13+ it is built in; this is a no-op on those versions.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- caretd lives entirely within its own schema.  Dropping the schema fully
-- uninstalls caretd without touching the host application's tables in public.
CREATE SCHEMA IF NOT EXISTS caretd;

-- domains — caretd's isolation boundary.
-- One row per SIP domain.  The host application maps its own tenants onto
-- domains; caretd is agnostic to tenancy.  `scope` is an opaque, non-FK
-- reference that the app may use to tag rows (e.g. its tenant UUID) so it
-- can bulk-delete caretd config with DELETE … WHERE scope = $1.
CREATE TABLE caretd.domains (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    domain      TEXT        NOT NULL,
    scope       TEXT,                          -- opaque app reference; NOT a FK
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT  domains_domain_unique UNIQUE (domain)
);

CREATE INDEX domains_scope_idx ON caretd.domains (scope);

-- endpoints — an AOR + auth identity, scoped to a domain.
-- ha1 stores the pre-computed MD5(username:realm:password) digest; plaintext
-- passwords are never stored.
CREATE TABLE caretd.endpoints (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id    UUID        NOT NULL
                                 REFERENCES caretd.domains(id) ON DELETE CASCADE,
    username     TEXT        NOT NULL,
    ha1          TEXT        NOT NULL,          -- MD5(user:realm:pass)
    display_name TEXT,
    codecs       TEXT[],                        -- allowed codecs, preference order
    max_contacts INT         NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT   endpoints_domain_username_unique UNIQUE (domain_id, username)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Dropping the schema removes every caretd object in one statement.
-- The host application's public schema and its tables are untouched.
DROP SCHEMA IF EXISTS caretd CASCADE;

-- +goose StatementEnd
