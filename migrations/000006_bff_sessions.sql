-- 000006_bff_sessions — the BFF's server-side sessions and pending logins
-- (RENT-BE-001 §4; backendkit bff PostgresStore and PostgresPendingLoginStore,
-- managed schema). Reversible: Down drops both tables.
--
-- Neither table carries owner_id: a session belongs to a Socrate identity
-- before any owner scope is chosen. Every token-bearing column is encrypted
-- by backendkit (AES-256-GCM under TENURE_SESSION_KEY), and only tenure_api
-- may read or write them (Q-86).

-- +goose Up
CREATE TABLE bff_store_sessions (
    id         text        PRIMARY KEY,
    data       bytea       NOT NULL,
    created_at timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    deleted_at timestamptz
);
CREATE INDEX bff_store_sessions_last_seen_idx ON bff_store_sessions (last_seen);

CREATE TABLE bff_store_pending_logins (
    state      text        PRIMARY KEY,
    data       bytea       NOT NULL,
    created_at timestamptz NOT NULL
);

REVOKE ALL ON bff_store_sessions, bff_store_pending_logins FROM tenure_jobs;
GRANT SELECT, INSERT, UPDATE, DELETE ON bff_store_sessions, bff_store_pending_logins TO tenure_api;

-- seed:legal_params:begin
-- seed:legal_params:end

-- +goose Down
DROP TABLE bff_store_pending_logins;
DROP TABLE bff_store_sessions;
