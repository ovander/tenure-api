-- 000002_billing — monthly fee, documents, delivery (RENT-BE-001 §2, §6; slice 3).
-- Reversible: the Down section drops everything this file creates.

-- +goose Up
CREATE TYPE billing_kind AS ENUM ('rent_call', 'receipt', 'regularisation', 'revision_notice', 'cancellation');
CREATE TYPE delivery_status AS ENUM ('pending', 'sent', 'failed', 'skipped');

-- The object version of the archived file (bucket versioning, Q-47).
ALTER TABLE documents ADD COLUMN object_version text NOT NULL DEFAULT '';

-- Per (owner, class, asset, year) sequence of document numbers, locked in the
-- issuing transaction so a failure gives the number back (RENT-BE-001 §6).
CREATE TABLE document_counters (
    owner_id   uuid NOT NULL REFERENCES owners (id),
    class      document_class NOT NULL,
    asset_code text NOT NULL,
    year       integer NOT NULL,
    last_seq   integer NOT NULL CHECK (last_seq > 0),
    PRIMARY KEY (owner_id, class, asset_code, year)
);

-- Rent calls, receipts, statements, revision notices and cancellations. Rows
-- are created issued and never change; a correction is a cancellation row
-- (cancels_id) followed by a new generation of the same lease, kind and period.
CREATE TABLE billing_documents (
    id             uuid PRIMARY KEY,
    owner_id       uuid NOT NULL REFERENCES owners (id),
    lease_id       uuid NOT NULL,
    kind           billing_kind NOT NULL,
    period         char(7) NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
    generation     integer NOT NULL DEFAULT 1 CHECK (generation > 0),
    number         text NOT NULL,
    lines          jsonb NOT NULL,
    total_cents    bigint NOT NULL,
    issued_at      timestamptz NOT NULL,
    cancels_id     uuid,
    document_id    uuid NOT NULL,
    input_snapshot jsonb NOT NULL,
    created_by     text NOT NULL,
    UNIQUE (id, owner_id),
    UNIQUE (owner_id, number),
    FOREIGN KEY (lease_id, owner_id) REFERENCES leases (id, owner_id),
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id),
    FOREIGN KEY (cancels_id, owner_id) REFERENCES billing_documents (id, owner_id),
    CHECK ((kind = 'cancellation') = (cancels_id IS NOT NULL))
);
CREATE UNIQUE INDEX billing_documents_one_per_generation
    ON billing_documents (lease_id, kind, period, generation) WHERE kind <> 'cancellation';
CREATE UNIQUE INDEX billing_documents_one_cancellation ON billing_documents (cancels_id) WHERE cancels_id IS NOT NULL;
CREATE INDEX billing_documents_period ON billing_documents (owner_id, period);

-- +goose StatementBegin
CREATE FUNCTION billing_documents_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'billing documents are immutable once issued' USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER billing_documents_immutable BEFORE UPDATE OR DELETE ON billing_documents
    FOR EACH ROW EXECUTE FUNCTION billing_documents_immutable();

-- Delivery of a document to a recipient (Q-24).
CREATE TABLE document_deliveries (
    id          uuid PRIMARY KEY,
    owner_id    uuid NOT NULL,
    document_id uuid NOT NULL,
    recipient   text NOT NULL,
    channel     text NOT NULL DEFAULT 'email',
    status      delivery_status NOT NULL DEFAULT 'pending',
    attempts    integer NOT NULL DEFAULT 0,
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    FOREIGN KEY (document_id, owner_id) REFERENCES documents (id, owner_id)
);
CREATE INDEX document_deliveries_document ON document_deliveries (document_id);

-- Row-level security, as for every owner table (000001).
-- +goose StatementBegin
CREATE FUNCTION tenure_owner_policies(t regclass) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY api_read ON %s FOR SELECT TO tenure_api
        USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) IS NOT NULL)', t);
    EXECUTE format('CREATE POLICY api_insert ON %s FOR INSERT TO tenure_api
        WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')', t);
    EXECUTE format('CREATE POLICY api_update ON %s FOR UPDATE TO tenure_api
        USING (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')
        WITH CHECK (owner_id = app_owner_id() AND tenure_scope_role(owner_id) = ''owner'')', t);
    EXECUTE format('CREATE POLICY jobs_scope ON %s TO tenure_jobs
        USING (owner_id = app_owner_id()) WITH CHECK (owner_id = app_owner_id())', t);
END
$$;
-- +goose StatementEnd
SELECT tenure_owner_policies('document_counters');
SELECT tenure_owner_policies('billing_documents');
SELECT tenure_owner_policies('document_deliveries');
DROP FUNCTION tenure_owner_policies(regclass);

-- Documents are issued by the API (owner actions) and by the scheduler.
GRANT SELECT, INSERT, UPDATE ON document_counters, document_deliveries TO tenure_api, tenure_jobs;
GRANT SELECT, INSERT ON billing_documents TO tenure_api, tenure_jobs;
GRANT INSERT ON documents TO tenure_jobs;
GRANT UPDATE ON leases TO tenure_jobs;
-- Manual IRL override by an admin (PUT /params/irl/{quarter}).
GRANT INSERT ON irl_index TO tenure_api;

-- Seed of legal_params added since 000001, generated from testdata/params/*.json
-- by `go test ./migrations -update`.
-- seed:legal_params:begin
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('archive_retention_years', '2025-01-01', '10'::jsonb, 'RENT-SPEC-005 (10 ans, au-delà des délais de prescription civile et fiscale; Code de commerce L123-22 pour les pièces comptables)', '');
INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES ('archive_retention_years', '2026-01-01', '10'::jsonb, 'RENT-SPEC-005 (10 ans, au-delà des délais de prescription civile et fiscale; Code de commerce L123-22 pour les pièces comptables)', '');
-- seed:legal_params:end

-- +goose Down
DELETE FROM legal_params WHERE key IN ('archive_retention_years');
REVOKE INSERT ON irl_index FROM tenure_api;
REVOKE UPDATE ON leases FROM tenure_jobs;
REVOKE INSERT ON documents FROM tenure_jobs;
DROP TABLE document_deliveries;
DROP TABLE billing_documents;
DROP FUNCTION billing_documents_immutable();
DROP TABLE document_counters;
ALTER TABLE documents DROP COLUMN object_version;
DROP TYPE delivery_status;
DROP TYPE billing_kind;
