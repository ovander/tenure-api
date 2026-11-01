-- 000008_archive — exports, retention clock, archive checks (RENT-SPEC-005,
-- RENT-SPEC-022; RENT-BE-001 §3, §5; slice 8). Reversible: Down restores the
-- trigger of 000001 and drops what this file creates.

-- +goose Up
CREATE TYPE export_state AS ENUM ('pending', 'ready', 'failed');

-- An export of a year or of an asset's whole history: a ZIP of the scope's
-- documents with an index CSV, built after the request commits (Q-91). The
-- ZIP is a copy, kept outside the locked archive under <owner_id>/exports/.
CREATE TABLE export_jobs (
    id             uuid PRIMARY KEY,
    owner_id       uuid NOT NULL REFERENCES owners (id),
    requested_by   text NOT NULL,
    role           text NOT NULL,
    year           integer,
    asset_id       uuid,
    state          export_state NOT NULL DEFAULT 'pending',
    object_key     text NOT NULL DEFAULT '',
    object_version text NOT NULL DEFAULT '',
    sha256         text NOT NULL DEFAULT '',
    size_bytes     bigint NOT NULL DEFAULT 0,
    documents      integer NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL,
    finished_at    timestamptz,
    UNIQUE (id, owner_id),
    FOREIGN KEY (asset_id, owner_id) REFERENCES assets (id, owner_id),
    CHECK ((year IS NULL) <> (asset_id IS NULL))
);
ALTER TABLE export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE export_jobs FORCE ROW LEVEL SECURITY;
-- Any role of the scope exports what it may read; each sees only its own
-- exports (the owner sees all of the scope's).
CREATE POLICY api_read ON export_jobs FOR SELECT TO tenure_api
    USING (owner_id = app_owner_id() AND (requested_by = app_actor_sub() OR tenure_scope_role(owner_id) = 'owner'));
CREATE POLICY api_insert ON export_jobs FOR INSERT TO tenure_api
    WITH CHECK (owner_id = app_owner_id() AND requested_by = app_actor_sub() AND tenure_scope_role(owner_id) IS NOT NULL);
CREATE POLICY api_update ON export_jobs FOR UPDATE TO tenure_api
    USING (owner_id = app_owner_id() AND requested_by = app_actor_sub())
    WITH CHECK (owner_id = app_owner_id() AND requested_by = app_actor_sub());
GRANT SELECT, INSERT, UPDATE ON export_jobs TO tenure_api;

-- The retention clock of a lease document or a supporting document starts
-- later than its archive (Q-68): retain_until may be set once, from unset,
-- by archive.retention; nothing else of an issued document ever changes.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'documents are never deleted' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.issued_at IS NOT NULL THEN
        IF OLD.retain_until IS NULL AND NEW.retain_until IS NOT NULL
            AND to_jsonb(NEW) - 'retain_until' = to_jsonb(OLD) - 'retain_until' THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'document % is issued and immutable', OLD.id USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
GRANT UPDATE (retain_until) ON documents TO tenure_jobs;

-- seed:legal_params:begin
-- seed:legal_params:end

-- +goose Down
REVOKE UPDATE (retain_until) ON documents FROM tenure_jobs;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'documents are never deleted' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.issued_at IS NOT NULL THEN
        RAISE EXCEPTION 'document % is issued and immutable', OLD.id USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
DROP TABLE export_jobs;
DROP TYPE export_state;
